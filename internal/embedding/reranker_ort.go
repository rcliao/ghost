//go:build ORT && cgo

package embedding

import (
	"os"
	"runtime"
	"strconv"

	"github.com/knights-analytics/hugot"
	"github.com/knights-analytics/hugot/options"
)

// makeRerankerSession creates a hugot session using the ONNX Runtime backend.
// Requires a system-installed onnxruntime shared library:
//
//	# macOS
//	brew install onnxruntime
//	# point Ghost at the DIRECTORY containing libonnxruntime.dylib
//	export GHOST_ONNXRUNTIME_PATH=/opt/homebrew/lib
//
//	# Linux (Debian/Ubuntu)
//	# install libonnxruntime-dev or download from https://github.com/microsoft/onnxruntime/releases
//	export GHOST_ONNXRUNTIME_PATH=/usr/lib/x86_64-linux-gnu
//
// (Note: WithOnnxLibraryPath takes a directory, not a file path.)
//
// Intra-op thread count is set to runtime.NumCPU() by default so the
// cross-encoder forward pass uses all cores. Override via
// GHOST_ORT_INTRA_THREADS / GHOST_ORT_INTER_THREADS if needed.
func makeRerankerSession() (*hugot.Session, error) {
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

const rerankerBackendName = "onnxruntime"
