package embedding

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/knights-analytics/hugot"
	"github.com/knights-analytics/hugot/pipelines"
)

const (
	defaultModel    = "sentence-transformers/all-MiniLM-L6-v2"
	defaultOnnxFile = "model.onnx"
	localDims       = 384
)

// LocalEmbedder runs all-MiniLM-L6-v2 locally via hugot (pure Go, no CGo).
// The model is downloaded on first use to ~/.ghost/models/.
type LocalEmbedder struct {
	modelsDir string

	mu       sync.Mutex
	session  *hugot.Session
	pipeline *pipelines.FeatureExtractionPipeline
	initErr  error
	inited   bool
}

// NewLocalEmbedder creates a local embedder. The model is lazily downloaded
// and the pipeline is lazily initialized on first Embed call.
func NewLocalEmbedder() *LocalEmbedder {
	dir := os.Getenv("GHOST_MODELS_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".ghost", "models")
	}
	return &LocalEmbedder{modelsDir: dir}
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
	modelPath := filepath.Join(e.modelsDir, "sentence-transformers_all-MiniLM-L6-v2")
	onnxPath := filepath.Join(modelPath, defaultOnnxFile)
	if _, err := os.Stat(onnxPath); os.IsNotExist(err) {
		opts := hugot.NewDownloadOptions()
		opts.OnnxFilePath = "onnx/" + defaultOnnxFile
		downloaded, err := hugot.DownloadModel(defaultModel, e.modelsDir, opts)
		if err != nil {
			e.initErr = fmt.Errorf("download model: %w", err)
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

func (e *LocalEmbedder) Dims() int { return localDims }

// Close releases the hugot session resources.
func (e *LocalEmbedder) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.session != nil {
		e.session.Destroy()
		e.session = nil
	}
}
