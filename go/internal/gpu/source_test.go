package gpu

import "arkstudio/go/internal/pyjson"

// gpuSource mirrors resources.GPUSource (go/internal/resources on
// feat/go-backend-cli); *Service must keep satisfying it.
type gpuSource interface {
	Servers() (*pyjson.Object, error)
	LastProbe() *pyjson.Object
	QueueJobs(projectID string) ([]any, error)
}

var _ gpuSource = (*Service)(nil)
