package embedding

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/knights-analytics/hugot"
	"github.com/knights-analytics/hugot/pipelines"
)

const (
	defaultOnnxFile = "model.onnx"
)

// localModelSpec defines a supported local embedding model.
type localModelSpec struct {
	hfName   string // HuggingFace model ID
	dims     int
	onnxPath string // path within HF repo (e.g. "onnx/model.onnx")
}

// supportedLocalModels maps short names to model specs.
var supportedLocalModels = map[string]localModelSpec{
	"all-MiniLM-L6-v2": {
		hfName:   "sentence-transformers/all-MiniLM-L6-v2",
		dims:     384,
		onnxPath: "onnx/" + defaultOnnxFile,
	},
	"gte-small": {
		hfName:   "thenlper/gte-small",
		dims:     384,
		onnxPath: "onnx/" + defaultOnnxFile,
	},
	"bge-base-en-v1.5": {
		hfName:   "BAAI/bge-base-en-v1.5",
		dims:     768,
		onnxPath: "onnx/" + defaultOnnxFile,
	},
}

// defaultLocalModel is the model used when GHOST_EMBED_MODEL_LOCAL is not set.
const defaultLocalModel = "all-MiniLM-L6-v2"

// LocalEmbedder runs embedding models locally via hugot (pure Go, no CGo).
// The model is downloaded on first use to ~/.ghost/models/.
type LocalEmbedder struct {
	modelsDir string
	modelName string
	spec      localModelSpec

	mu       sync.Mutex
	session  *hugot.Session
	pipeline *pipelines.FeatureExtractionPipeline
	initErr  error
	inited   bool
}

// NewLocalEmbedder creates a local embedder using the default model.
// Override with GHOST_EMBED_MODEL_LOCAL env var.
// Supported: all-MiniLM-L6-v2 (default), gte-small, bge-base-en-v1.5
func NewLocalEmbedder() *LocalEmbedder {
	dir := os.Getenv("GHOST_MODELS_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".ghost", "models")
	}
	modelName := os.Getenv("GHOST_EMBED_MODEL_LOCAL")
	if modelName == "" {
		modelName = defaultLocalModel
	}
	spec, ok := supportedLocalModels[modelName]
	if !ok {
		// Fallback to default if unknown model
		spec = supportedLocalModels[defaultLocalModel]
		modelName = defaultLocalModel
	}
	return &LocalEmbedder{modelsDir: dir, modelName: modelName, spec: spec}
}

func (e *LocalEmbedder) init() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.inited {
		return e.initErr
	}
	e.inited = true

	if err := os.MkdirAll(e.modelsDir, 0o755); err != nil {
		e.initErr = fmt.Errorf("create models dir: %w", err)
		return e.initErr
	}

	// Download model if not present
	// Model dir name: replace / with _ in HF name
	modelDirName := strings.ReplaceAll(e.spec.hfName, "/", "_")
	modelPath := filepath.Join(e.modelsDir, modelDirName)
	onnxPath := filepath.Join(modelPath, defaultOnnxFile)
	if _, err := os.Stat(onnxPath); os.IsNotExist(err) {
		opts := hugot.NewDownloadOptions()
		opts.OnnxFilePath = e.spec.onnxPath
		downloaded, err := hugot.DownloadModel(e.spec.hfName, e.modelsDir, opts)
		if err != nil {
			e.initErr = fmt.Errorf("download model %s: %w", e.spec.hfName, err)
			return e.initErr
		}
		modelPath = downloaded
	}

	// Create pure Go session (no CGo, no ONNX Runtime)
	session, err := hugot.NewGoSession()
	if err != nil {
		e.initErr = fmt.Errorf("create session: %w", err)
		return e.initErr
	}
	e.session = session

	config := hugot.FeatureExtractionConfig{
		ModelPath:    modelPath,
		Name:         "ghost-embedder",
		OnnxFilename: defaultOnnxFile,
	}
	pipeline, err := hugot.NewPipeline[*pipelines.FeatureExtractionPipeline](session, config)
	if err != nil {
		session.Destroy()
		e.initErr = fmt.Errorf("create pipeline: %w", err)
		return e.initErr
	}
	e.pipeline = pipeline

	return nil
}

func (e *LocalEmbedder) Embed(ctx context.Context, text string) (vec Vector, err error) {
	if initErr := e.init(); initErr != nil {
		return nil, initErr
	}

	// The underlying tokenizer can panic on certain inputs (e.g. unusual Unicode).
	// Recover gracefully so callers can skip problematic texts.
	defer func() {
		if r := recover(); r != nil {
			vec = nil
			err = fmt.Errorf("embed panic: %v", r)
		}
	}()

	result, err := e.pipeline.RunPipeline([]string{text})
	if err != nil {
		return nil, fmt.Errorf("embed: %w", err)
	}
	if len(result.Embeddings) == 0 || len(result.Embeddings[0]) == 0 {
		return nil, fmt.Errorf("embed: empty result")
	}

	// Convert []float64 to []float32
	emb64 := result.Embeddings[0]
	vec = make(Vector, len(emb64))
	for i, v := range emb64 {
		vec[i] = float32(v)
	}
	return vec, nil
}

func (e *LocalEmbedder) EmbedBatch(ctx context.Context, texts []string) ([]Vector, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if initErr := e.init(); initErr != nil {
		return nil, initErr
	}

	// hugot's RunPipeline natively supports batch input
	result, err := e.pipeline.RunPipeline(texts)
	if err != nil {
		return nil, fmt.Errorf("embed batch: %w", err)
	}
	if len(result.Embeddings) != len(texts) {
		return nil, fmt.Errorf("embed batch: expected %d embeddings, got %d", len(texts), len(result.Embeddings))
	}

	vecs := make([]Vector, len(texts))
	for i, emb64 := range result.Embeddings {
		vec := make(Vector, len(emb64))
		for j, v := range emb64 {
			vec[j] = float32(v)
		}
		vecs[i] = vec
	}
	return vecs, nil
}

func (e *LocalEmbedder) Dims() int { return e.spec.dims }

// Close releases the hugot session resources.
func (e *LocalEmbedder) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.session != nil {
		e.session.Destroy()
		e.session = nil
	}
}
