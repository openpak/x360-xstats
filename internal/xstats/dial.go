package xstats

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	pb "github.com/openpak/x360-xstats/internal/leaderboardspb"
)

// maxReply raises gRPC's 4 MB default reply size (docs/adapter-guide.md, "Message sizes").
const maxReply = 16 << 20

// DialLeaderboards connects to the neutral leaderboards service and presents the internal key on
// every call. extra lets tests add a dialer.
func DialLeaderboards(addr, key string, extra ...grpc.DialOption) (pb.LeaderboardsClient, *grpc.ClientConn, error) {
	opts := append([]grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(maxReply)),
		grpc.WithUnaryInterceptor(func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, inv grpc.UnaryInvoker, o ...grpc.CallOption) error {
			return inv(metadata.AppendToOutgoingContext(ctx, "x-internal-key", key), method, req, reply, cc, o...)
		}),
	}, extra...)
	conn, err := grpc.NewClient(addr, opts...)
	if err != nil {
		return nil, nil, err
	}
	return pb.NewLeaderboardsClient(conn), conn, nil
}
