package awg

import (
	"context"

	awgv1 "github.com/mrcsin/awg-grpc/gen/awg/v1"
	"google.golang.org/grpc"
)

type fakeClient struct {
	status    func() (*awgv1.GetStatusResponse, error)
	apply     func(*awgv1.ApplyPeersRequest) (*awgv1.ApplyPeersResponse, error)
	listPeers func(*awgv1.ListPeersRequest) (*awgv1.ListPeersResponse, error)

	statusCalls int
	applied     []*awgv1.ApplyPeersRequest
}

func (f *fakeClient) GetStatus(_ context.Context, _ *awgv1.GetStatusRequest, _ ...grpc.CallOption) (*awgv1.GetStatusResponse, error) {
	f.statusCalls++
	return f.status()
}

func (f *fakeClient) ApplyPeers(_ context.Context, in *awgv1.ApplyPeersRequest, _ ...grpc.CallOption) (*awgv1.ApplyPeersResponse, error) {
	f.applied = append(f.applied, in)
	if f.apply == nil {
		return &awgv1.ApplyPeersResponse{}, nil
	}
	return f.apply(in)
}

func (f *fakeClient) ListPeers(_ context.Context, in *awgv1.ListPeersRequest, _ ...grpc.CallOption) (*awgv1.ListPeersResponse, error) {
	return f.listPeers(in)
}
