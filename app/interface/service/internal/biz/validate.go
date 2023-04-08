package biz

import (
	"github.com/go-kratos/kratos/v2/log"
	_ "github.com/go-playground/locales/zh"
	_ "github.com/go-playground/universal-translator"
	_ "github.com/go-playground/validator/v10"
	_ "github.com/go-playground/validator/v10/translations/zh"
	"github.com/openapi-platform/openapi-backend/app/interface/service/internal/conf"
)

type ValidateUseCase struct{}

func NewValidateUseCase(conf *conf.Auth, repo AuthRepo, re Recovery, userRepo UserRepo, tm Transaction, logger log.Logger) *ValidateUseCase {
	return &ValidateUseCase{}
}
