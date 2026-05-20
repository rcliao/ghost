//go:build ORT && cgo

package embedding

import (
	"os"
	"runtime"
	"strconv"

	"github.com/knights-analytics/hugot"
	"github.com/knights-analytics/hugot/options"
)

// makeEmbedderSession uses the ONNX Runtime backend with all CPU cores for
// the embedder forward pass. See reranker_ort.go for setup; the same
// onnxruntime + libtokenizers install covers both.
func makeEmbedderSession() (*hugot.Session, error) {
	intra := runtime.NumCPU()
	if v := os.Getenv("GHOST_ORT_INTRA_THREADS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			intra = n
		}
	}
	inter := 1
	if v := os.Getenv("GHOST_ORT_INTER_THREADS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			inter = n
		}
	}

	opts := []options.WithOption{
		options.WithIntraOpNumThreads(intra),
		options.WithInterOpNumThreads(inter),
	}
	if lib := os.Getenv("GHOST_ONNXRUNTIME_PATH"); lib != "" {
		opts = append(opts, options.WithOnnxLibraryPath(lib))
	}
	return hugot.NewORTSession(opts...)
}
