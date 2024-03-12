package kni

import (
	"context"

	"github.com/MikeZappa87/kni-api/pkg/apis/runtime/beta"
	"google.golang.org/grpc"
)

type KNIService interface {
	AttachInterface(ctx context.Context, in *beta.AttachInterfaceRequest,
		opts ...grpc.CallOption) (*beta.AttachInterfaceResponse, error)

	DetachInterface(ctx context.Context, sandboxId string) error

	QueryPodNetwork(ctx context.Context, sandboxId string) (*beta.QueryPodNetworkResponse, error)

	SetupNodeNetwork(ctx context.Context, in *beta.SetupNodeNetworkRequest,
		opts ...grpc.CallOption) (*beta.SetupNodeNetworkResponse, error)

	QueryNodeNetworks(ctx context.Context) (*beta.QueryNodeNetworksResponse, error)

	Up() bool

	CreateNetwork(ctx context.Context, req beta.CreateNetworkRequest) (*beta.CreateNetworkResponse, error)

	DeleteNetworkById(ctx context.Context, sandboxId string) error

	DeleteNetworkByPodName(ctx context.Context, name, namespace string) error
}
