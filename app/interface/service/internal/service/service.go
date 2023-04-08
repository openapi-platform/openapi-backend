package service

import (
	"context"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/google/wire"
	v1 "github.com/openapi-platform/openapi-backend/api/interface/service/v1"
	"github.com/openapi-platform/openapi-backend/app/interface/service/internal/biz"
	"google.golang.org/protobuf/types/known/emptypb"
)

var ProviderSet = wire.NewSet(NewUserService)

type UserService struct {
	v1.UnimplementedUserServer
	uc  *biz.UserUseCase
	ac  *biz.AuthUseCase
	log *log.Helper
}

func NewUserService(uc *biz.UserUseCase, ac *biz.AuthUseCase, logger log.Logger) *UserService {
	return &UserService{
		log: log.NewHelper(log.With(logger, "module", "user/service")),
		uc:  uc,
		ac:  ac,
	}
}

func (s *UserService) GetHealth(_ context.Context, _ *emptypb.Empty) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}
