//go:build wireinject
// +build wireinject

// The build tag makes sure the stub is not built in the final build.

package main

import (
	"github.com/go-kratos/kratos/contrib/registry/nacos/v2"
	"github.com/go-kratos/kratos/v2"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/google/wire"
	"github.com/openapi-platform/openapi-backend/app/user/service/internal/biz"
	"github.com/openapi-platform/openapi-backend/app/user/service/internal/conf"
	"github.com/openapi-platform/openapi-backend/app/user/service/internal/data"
	"github.com/openapi-platform/openapi-backend/app/user/service/internal/server"
	"github.com/openapi-platform/openapi-backend/app/user/service/internal/service"
)

// wireApp init kratos application.
func wireApp(*conf.Server, *conf.Data, *conf.Auth, log.Logger, *nacos.Registry) (*kratos.App, func(), error) {
	panic(wire.Build(server.ProviderSet, data.ProviderSet, biz.ProviderSet, service.ProviderSet, newApp))
}
