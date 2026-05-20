//go:build !ORT

package embedding

import "github.com/knights-analytics/hugot"

// makeEmbedderSession uses hugot's pure-Go simplego backend. Single-threaded;
// embedding ~10/sec on M-series CPU. Acceptable for small corpora.
//
// For large corpora (e.g. LongMemEval _M with ~230k unique texts), build with
// `-tags=ORT` to use a threaded ONNX Runtime — see local_ort.go.
func makeEmbedderSession() (*hugot.Session, error) {
	return hugot.NewGoSession()
}
