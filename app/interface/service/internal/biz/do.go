package biz

import (
	"time"
)

type User struct {
	Uuid     string
	Phone    string
	Email    string
	Qq       string
	Wechat   string
	Gitee    int32
	Github   int32
	Password string
}

type Profile struct {
	Created   string
	Updated   string
	Uuid      string
	Username  string
	Avatar    string
	School    string
	Company   string
	Job       string
	Homepage  string
	Github    string
	Gitee     string
	Introduce string
}

type UserSearch struct {
	Uuid      string
	Username  string
	Introduce string
}

//easyjson:json
type ProfileUpdate struct {
	Profile
	Mode   string
	Status int32
}

type Github struct {
	Token    string
	Avatar   string
	Register bool
	Uuid     string
}

type Credentials struct {
	TmpSecretID  string
	TmpSecretKey string
	SessionToken string
	StartTime    int64
	ExpiredTime  int64
}

type Follow struct {
	Id       int32
	Update   time.Time
	Follow   string
	Followed string
	Status   int32
}

type Follows struct {
	Uuid   string
	Follow int32
}

//easyjson:json
type ImageReview struct {
	Id       int32
	CreateAt string
	Uuid     string
	JobId    string
	Url      string
	Label    string
	Result   int32
	Category string
	SubLabel string
	Mode     string
	Score    int32
}

//easyjson:json
type UserSearchMap struct {
	Username  string
	Introduce string
}

//easyjson:json
type ProfileUpdateMap struct {
	Username  string
	Introduce string
}

//easyjson:json
type SendUserStatisticMap struct {
	Follow   string
	Followed string
	Mode     string
}

//easyjson:json
type SetFollowMap struct {
	Uuid   string
	UserId string
	Mode   string
}
