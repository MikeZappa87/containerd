package kni

import (
	"context"
	"os"
	"time"

	"github.com/MikeZappa87/kni-api/pkg/apis/runtime/beta"
	"github.com/containerd/containerd/v2/integration/remote/util"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials/insecure"
	"k8s.io/klog/v2"
)

type KNINetworkService struct {
	beta.KNIClient
}

type KNIConfig struct {
	SocketPath string
}

const (
	// connection parameters
	maxBackoffDelay      = 3 * time.Second
	baseBackoffDelay     = 100 * time.Millisecond
	minConnectionTimeout = 5 * time.Second
	maxMsgSize           = 1024 * 1024 * 16
)

// NewRemoteRuntimeService creates a new networkremote.KNIService.
func NewNetworkRuntimeService(endpoint string, connectionTimeout time.Duration) (*KNINetworkService, error) {
	klog.V(3).InfoS("Connecting to runtime service", "endpoint", endpoint)
	addr, dialer, err := util.GetAddressAndDialer(endpoint)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), connectionTimeout)
	defer cancel()

	var dialOpts []grpc.DialOption
	dialOpts = append(dialOpts,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(dialer),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(maxMsgSize)))

	connParams := grpc.ConnectParams{
		Backoff: backoff.DefaultConfig,
	}
	connParams.MinConnectTimeout = minConnectionTimeout
	connParams.Backoff.BaseDelay = baseBackoffDelay
	connParams.Backoff.MaxDelay = maxBackoffDelay
	dialOpts = append(dialOpts,
		grpc.WithConnectParams(connParams),
	)

	conn, err := grpc.DialContext(ctx, addr, dialOpts...)
	if err != nil {
		klog.ErrorS(err, "Connect remote runtime failed", "address", addr)
		return nil, err
	}

	return &KNINetworkService{
		beta.NewKNIClient(conn),
	}, nil
}

func (m *KNINetworkService) AttachInterface(ctx context.Context, in *beta.AttachInterfaceRequest, opts ...grpc.CallOption) (*beta.AttachInterfaceResponse, error) {
	return m.KNIClient.AttachInterface(ctx, in)
}

func (m *KNINetworkService) DetachInterface(ctx context.Context, in *beta.DetachInterfaceRequest, opts ...grpc.CallOption) (*beta.DetachInterfaceResponse, error) {
	return m.KNIClient.DetachInterface(ctx, in)
}

func (m *KNINetworkService) QueryNodeNetworks(ctx context.Context, in *beta.QueryNodeNetworksRequest, opts ...grpc.CallOption) (*beta.QueryNodeNetworksResponse, error) {
	return m.KNIClient.QueryNodeNetworks(ctx, in)
}

func (m *KNINetworkService) QueryPodNetwork(ctx context.Context, in *beta.QueryPodNetworkRequest, opts ...grpc.CallOption) (*beta.QueryPodNetworkResponse, error) {
	return m.KNIClient.QueryPodNetwork(ctx, in)
}

func (m *KNINetworkService) SetupNodeNetwork(ctx context.Context, in *beta.SetupNodeNetworkRequest, opts ...grpc.CallOption) (*beta.SetupNodeNetworkResponse, error) {
	return m.KNIClient.SetupNodeNetwork(ctx, in)
}

func (m *KNINetworkService) Up() bool {
	if _, err := os.Stat("/tmp/kni.sock"); err != nil {
		return false
	}

	return true
}

func (m *KNINetworkService) CreateNetwork(ctx context.Context, req *beta.CreateNetworkRequest, opts ...grpc.CallOption) (*beta.CreateNetworkResponse, error) {
	return m.KNIClient.CreateNetwork(ctx, req)
}

func (m *KNINetworkService) DeleteNetworkById(ctx context.Context, podSandBoxID string) error {
	_, err := m.KNIClient.DeleteNetwork(ctx, &beta.DeleteNetworkRequest{
		Id: podSandBoxID,
	})

	return err
}

func (m *KNINetworkService) DeleteNetworkByPodName(ctx context.Context, name, namespace string) error {
	_, err := m.KNIClient.DeleteNetwork(ctx, &beta.DeleteNetworkRequest{
		Name:      name,
		Namespace: namespace,
	})

	return err
}
