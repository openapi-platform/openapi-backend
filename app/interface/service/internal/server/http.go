package server

import (
	"context"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/middleware/logging"
	"github.com/go-kratos/kratos/v2/middleware/recovery"
	"github.com/go-kratos/kratos/v2/middleware/validate"
	"github.com/go-kratos/kratos/v2/transport/http"
	v1 "github.com/openapi-platform/openapi-backend/api/interface/service/v1"
	"github.com/openapi-platform/openapi-backend/app/interface/service/internal/conf"
	"github.com/openapi-platform/openapi-backend/app/interface/service/internal/service"
	"github.com/openapi-platform/openapi-backend/pkg/responce"
)

// NewHTTPServer new a HTTP user.
func NewHTTPServer(c *conf.Server, userService *service.UserService, logger log.Logger) *http.Server {
	var opts = []http.ServerOption{
		http.Middleware(
			recovery.Recovery(recovery.WithHandler(func(ctx context.Context, req, err interface{}) error {
				l := log.NewHelper(log.With(logger, "message", "panic"))
				l.Error(err)
				return nil
			})),
			responce.Server(),
			logging.Server(log.NewFilter(logger, log.FilterLevel(log.LevelError))),
			validate.Validator(),
		),
	}
	if c.Http.Network != "" {
		opts = append(opts, http.Network(c.Http.Network))
	}
	if c.Http.Addr != "" {
		opts = append(opts, http.Address(c.Http.Addr))
	}
	if c.Http.Timeout != nil {
		opts = append(opts, http.Timeout(c.Http.Timeout.AsDuration()))
	}
	srv := http.NewServer(opts...)
	v1.RegisterUserHTTPServer(srv, userService)
	return srv
}
