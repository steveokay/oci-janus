package grpc

import (
	"context"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

// CachableMethod configures caching for one gRPC method.
type CachableMethod struct {
	// TTL is how long the cached response is valid.
	TTL time.Duration
	// KeyFunc derives the cache key suffix from the incoming request.
	// The full Redis key will be "grpc:<full_method>:<KeyFunc(req)>".
	// Return "" to skip caching for this particular request.
	KeyFunc func(req proto.Message) string
	// New returns a fresh empty proto.Message of the response type.
	// Used to unmarshal cache hits into the correct concrete type.
	New func() proto.Message
}

// CacheInterceptor returns a server-side unary interceptor that transparently
// caches responses for registered read-only methods. Only exact FullMethod
// matches are cached; all other methods pass through untouched.
//
// Cache misses call the real handler and store the result. Corrupted cache
// entries are deleted and the handler is called as a fallback — a cache failure
// never causes a request failure.
//
// Invalidation is TTL-based. Short TTLs (30s for repos/tags, 5m for manifests)
// bound staleness without requiring write-path cache busting.
func CacheInterceptor(rdb *redis.Client, methods map[string]CachableMethod) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		mc, ok := methods[info.FullMethod]
		if !ok {
			return handler(ctx, req)
		}

		protoReq, ok := req.(proto.Message)
		if !ok {
			return handler(ctx, req)
		}

		keySuffix := mc.KeyFunc(protoReq)
		if keySuffix == "" {
			return handler(ctx, req)
		}
		redisKey := "grpc:" + info.FullMethod + ":" + keySuffix

		// Try cache first.
		if data, err := rdb.Get(ctx, redisKey).Bytes(); err == nil {
			msg := mc.New()
			if unmarshalErr := proto.Unmarshal(data, msg); unmarshalErr == nil {
				return msg, nil
			}
			// Corrupted entry — evict and fall through to handler.
			_ = rdb.Del(ctx, redisKey).Err()
		}

		// Cache miss: invoke the real handler.
		resp, err := handler(ctx, req)
		if err != nil || resp == nil {
			return resp, err
		}

		// Store the response. A storage failure is non-fatal — log and continue.
		if protoResp, ok := resp.(proto.Message); ok {
			if data, marshalErr := proto.Marshal(protoResp); marshalErr == nil {
				if setErr := rdb.Set(ctx, redisKey, data, mc.TTL).Err(); setErr != nil {
					slog.WarnContext(ctx, "cache: failed to store response",
						"method", info.FullMethod, "error", setErr)
				}
			}
		}
		return resp, nil
	}
}
