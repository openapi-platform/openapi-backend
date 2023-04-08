package data

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/apache/rocketmq-client-go/v2/primitive"
	"github.com/elastic/go-elasticsearch/v7/esapi"
	kerrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-redis/redis/v8"
	"github.com/openapi-platform/openapi-backend/app/user/service/internal/biz"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strconv"
	"time"
)

var _ biz.UserRepo = (*userRepo)(nil)

type userRepo struct {
	data *Data
	log  *log.Helper
}

func NewUserRepo(data *Data, logger log.Logger) biz.UserRepo {
	return &userRepo{
		data: data,
		log:  log.NewHelper(log.With(logger, "module", "user/data/user")),
	}
}

func (r *userRepo) GetAccount(ctx context.Context, uuid string) (*biz.User, error) {
	user := &User{}
	err := r.data.db.WithContext(ctx).Select("password", "phone", "email", "wechat", "qq", "github", "gitee").Where("uuid = ?", uuid).First(user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, kerrors.NotFound("user not found from db", fmt.Sprintf("uuid(%v)", uuid))
	}
	if err != nil {
		return nil, errors.Wrapf(err, fmt.Sprintf("fail to get account: uuid(%v)", uuid))
	}
	return &biz.User{
		Phone:    user.Phone,
		Email:    user.Email,
		Qq:       user.Qq,
		Wechat:   user.Wechat,
		Gitee:    user.Gitee,
		Github:   user.Github,
		Password: user.Password,
	}, nil
}

func (r *userRepo) GetProfile(ctx context.Context, uuid string) (*biz.Profile, error) {
	key := "profile_" + uuid
	target, err := r.getProfileFromCache(ctx, key)
	if kerrors.IsNotFound(err) {
		profile := &Profile{}
		err = r.data.db.WithContext(ctx).Where("uuid = ?", uuid).First(profile).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, kerrors.NotFound("profile not found from db", fmt.Sprintf("uuid(%v)", uuid))
		}
		if err != nil {
			return nil, errors.Wrapf(err, fmt.Sprintf("fail to get profile: uuid(%v)", uuid))
		}
		target = profile
		r.setProfileToCache(ctx, profile, key)
	}
	if err != nil {
		return nil, err
	}
	return &biz.Profile{
		Created:   target.CreatedAt.Format("2006-01-02 15:04:05"),
		Updated:   strconv.FormatInt(target.Updated, 10),
		Uuid:      target.Uuid,
		Username:  target.Username,
		Avatar:    target.Avatar,
		School:    target.School,
		Company:   target.Company,
		Job:       target.Job,
		Homepage:  target.Homepage,
		Github:    target.Github,
		Gitee:     target.Gitee,
		Introduce: target.Introduce,
	}, nil
}

func (r *userRepo) GetProfileList(ctx context.Context, uuids []string) ([]*biz.Profile, error) {
	exists, unExists, err := r.profileListExist(ctx, uuids)
	if err != nil {
		return nil, err
	}

	profileList := make([]*biz.Profile, 0, cap(exists))
	g, _ := errgroup.WithContext(ctx)
	g.Go(r.data.GroupRecover(ctx, func(ctx context.Context) error {
		if len(exists) == 0 {
			return nil
		}
		return r.getProfileListFromCache(ctx, exists, &profileList)
	}))
	g.Go(r.data.GroupRecover(ctx, func(ctx context.Context) error {
		if len(unExists) == 0 {
			return nil
		}
		return r.getProfileListFromDb(ctx, unExists, &profileList)
	}))

	err = g.Wait()
	if err != nil {
		return nil, err
	}

	return profileList, nil
}

func (r *userRepo) profileListExist(ctx context.Context, uuids []string) ([]string, []string, error) {
	cmd, err := r.data.redisCli.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		for _, item := range uuids {
			pipe.Exists(ctx, "profile_"+item)
		}
		return nil
	})
	if err != nil {
		return nil, nil, errors.Wrapf(err, fmt.Sprintf("fail to check if user profile exist from cache: uuids(%v)", uuids))
	}

	exists := make([]string, 0, len(cmd))
	unExists := make([]string, 0, len(cmd))
	for index, item := range cmd {
		exist := item.(*redis.IntCmd).Val()
		if exist == 1 {
			exists = append(exists, uuids[index])
		} else {
			unExists = append(unExists, uuids[index])
		}
	}
	return exists, unExists, nil
}

func (r *userRepo) getProfileListFromCache(ctx context.Context, exists []string, profileList *[]*biz.Profile) error {
	cmd, err := r.data.redisCli.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		for _, uuid := range exists {
			pipe.Get(ctx, "profile_"+uuid)
		}
		return nil
	})
	if err != nil {
		return errors.Wrapf(err, fmt.Sprintf("fail to get user profile list from cache: uuids(%v)", exists))
	}

	for _, item := range cmd {
		var cacheProfile = &Profile{}
		result := item.(*redis.StringCmd).Val()
		err = cacheProfile.UnmarshalJSON([]byte(result))
		if err != nil {
			return errors.Wrapf(err, fmt.Sprintf("json unmarshal error: profile(%v)", result))
		}
		*profileList = append(*profileList, &biz.Profile{
			Uuid:      cacheProfile.Uuid,
			Username:  cacheProfile.Username,
			Introduce: cacheProfile.Introduce,
		})
	}
	return nil
}

func (r *userRepo) getProfileListFromDb(ctx context.Context, unExists []string, profileList *[]*biz.Profile) error {
	list := make([]*Profile, 0, cap(unExists))
	err := r.data.db.WithContext(ctx).Select("uuid", "username", "introduce").Where("uuid IN ?", unExists).Find(&list).Error
	if err != nil {
		return errors.Wrapf(err, fmt.Sprintf("fail to get profile list from db: uuids(%v)", unExists))
	}

	for _, item := range list {
		*profileList = append(*profileList, &biz.Profile{
			Uuid:      item.Uuid,
			Username:  item.Username,
			Introduce: item.Introduce,
		})
	}

	if len(list) != 0 {
		newCtx, _ := context.WithTimeout(context.Background(), time.Second*2)
		go r.data.Recover(newCtx, func(ctx context.Context) {
			r.setProfileListToCache(list)
		})()
	}
	return nil
}

func (r *userRepo) setProfileListToCache(profileList []*Profile) {
	ctx := context.Background()
	_, err := r.data.redisCli.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		for _, item := range profileList {
			key := "profile_" + item.Uuid
			marshal, err := item.MarshalJSON()
			if err != nil {
				return errors.Wrapf(err, "fail to set user profile to json: json.Marshal(%v), error(%v)", item, err)
			}
			pipe.SetNX(ctx, key, marshal, time.Minute*30)
		}
		return nil
	})

	if err != nil {
		r.log.Errorf("fail to set user profile to cache, err(%s)", err.Error())
	}
}

func (r *userRepo) GetProfileUpdate(ctx context.Context, uuid string) (*biz.ProfileUpdate, error) {
	profile := &ProfileUpdate{}
	pu := &biz.ProfileUpdate{}
	err := r.data.db.WithContext(ctx).Where("uuid = ?", uuid).First(profile).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, kerrors.NotFound("profile update not found from db", fmt.Sprintf("uuid(%v)", uuid))
	}
	if err != nil {
		return nil, errors.Wrapf(err, fmt.Sprintf("fail to get profile update: uuid(%v)", uuid))
	}
	pu.Updated = strconv.FormatInt(profile.Updated, 10)
	pu.Uuid = profile.Uuid
	pu.Username = profile.Username
	pu.Avatar = profile.Avatar
	pu.School = profile.School
	pu.Company = profile.Company
	pu.Job = profile.Job
	pu.Homepage = profile.Homepage
	pu.Introduce = profile.Introduce
	pu.Github = profile.Github
	pu.Gitee = profile.Gitee
	pu.Status = profile.Status
	return pu, nil
}

func (r *userRepo) GetFollowList(ctx context.Context, page int32, uuid string) ([]*biz.Follow, error) {
	follow, err := r.getFollowListFromCache(ctx, page, uuid)
	if err != nil {
		return nil, err
	}

	size := len(follow)
	if size != 0 {
		return follow, nil
	}

	follow, err = r.getFollowFromDB(ctx, page, uuid)
	if err != nil {
		return nil, err
	}

	size = len(follow)
	if size != 0 {
		newCtx, _ := context.WithTimeout(context.Background(), time.Second*2)
		go r.data.Recover(newCtx, func(ctx context.Context) {
			r.setFollowToCache(uuid, follow)
		})()
	}
	return follow, nil
}

func (r *userRepo) getFollowListFromCache(ctx context.Context, page int32, uuid string) ([]*biz.Follow, error) {
	if page < 1 {
		page = 1
	}
	index := int64(page - 1)
	list, err := r.data.redisCli.ZRevRange(ctx, "follow_"+uuid, index*10, index*10+9).Result()
	if err != nil {
		return nil, errors.Wrapf(err, fmt.Sprintf("fail to get user follow from cache: uuid(%s), page(%v)", uuid, page))
	}

	follow := make([]*biz.Follow, 0, len(list))
	for _, item := range list {
		follow = append(follow, &biz.Follow{
			Follow: item,
		})
	}
	return follow, nil
}

func (r *userRepo) getFollowFromDB(ctx context.Context, page int32, uuid string) ([]*biz.Follow, error) {
	if page < 1 {
		page = 1
	}
	index := int(page - 1)
	list := make([]*Follow, 0)
	handle := r.data.db.WithContext(ctx).Select("follow", "id", "updated_at").Where("followed = ? and status = ?", uuid, 1).Order("updated_at desc").Offset(index * 10).Limit(10).Find(&list)
	err := handle.Error
	if err != nil {
		return nil, errors.Wrapf(err, fmt.Sprintf("fail to get follow list from db: uuid(%s)", uuid))
	}
	follows := make([]*biz.Follow, 0, len(list))
	for _, item := range list {
		follows = append(follows, &biz.Follow{
			Follow: item.Follow,
			Id:     int32(item.ID),
			Update: item.UpdatedAt,
		})
	}
	return follows, nil
}

func (r *userRepo) setFollowToCache(uuid string, follow []*biz.Follow) {
	ctx := context.Background()
	_, err := r.data.redisCli.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		z := make([]*redis.Z, 0, len(follow))
		for _, item := range follow {
			z = append(z, &redis.Z{
				//Score:  float64(item.Id),
				Score:  float64(item.Update.Unix()),
				Member: item.Follow,
			})
		}
		pipe.ZAddNX(ctx, "follow_"+uuid, z...)
		pipe.Expire(ctx, "follow_"+uuid, time.Minute*30)
		return nil
	})
	if err != nil {
		r.log.Errorf("fail to set user follow to cache: uuid(%s), follow(%v), err(%v)", follow, err)
	}
}

func (r *userRepo) GetFollowListCount(ctx context.Context, uuid string) (int32, error) {
	var count int64
	err := r.data.db.WithContext(ctx).Model(&Follow{}).Where("followed = ? and status = ?", uuid, 1).Count(&count).Error
	if err != nil {
		return 0, errors.Wrapf(err, fmt.Sprintf("fail to get follow list count from db: followed(%v)", uuid))
	}
	return int32(count), nil
}

func (r *userRepo) GetFollowedList(ctx context.Context, page int32, uuid string) ([]*biz.Follow, error) {
	followed, err := r.getFollowedListFromCache(ctx, page, uuid)
	if err != nil {
		return nil, err
	}

	size := len(followed)
	if size != 0 {
		return followed, nil
	}

	followed, err = r.getFollowedFromDB(ctx, page, uuid)
	if err != nil {
		return nil, err
	}

	size = len(followed)
	if size != 0 {
		newCtx, _ := context.WithTimeout(context.Background(), time.Second*2)
		go r.data.Recover(newCtx, func(ctx context.Context) {
			r.setFollowedToCache(uuid, followed)
		})()
	}
	return followed, nil
}

func (r *userRepo) getFollowedListFromCache(ctx context.Context, page int32, uuid string) ([]*biz.Follow, error) {
	if page < 1 {
		page = 1
	}
	index := int64(page - 1)
	list, err := r.data.redisCli.ZRevRange(ctx, "followed_"+uuid, index*10, index*10+9).Result()
	if err != nil {
		return nil, errors.Wrapf(err, fmt.Sprintf("fail to get user followed from cache: uuid(%s), page(%v)", uuid, page))
	}

	followed := make([]*biz.Follow, 0, len(list))
	for _, item := range list {
		followed = append(followed, &biz.Follow{
			Followed: item,
		})
	}
	return followed, nil
}

func (r *userRepo) getFollowedFromDB(ctx context.Context, page int32, uuid string) ([]*biz.Follow, error) {
	if page < 1 {
		page = 1
	}
	index := int(page - 1)
	list := make([]*Follow, 0)
	handle := r.data.db.WithContext(ctx).Select("followed", "id", "updated_at").Where("follow = ? and status = ?", uuid, 1).Order("updated_at desc").Offset(index * 10).Limit(10).Find(&list)
	err := handle.Error
	if err != nil {
		return nil, errors.Wrapf(err, fmt.Sprintf("fail to get followed list from db: uuid(%s)", uuid))
	}
	follows := make([]*biz.Follow, 0, len(list))
	for _, item := range list {
		follows = append(follows, &biz.Follow{
			Followed: item.Followed,
			Id:       int32(item.ID),
			Update:   item.UpdatedAt,
		})
	}
	return follows, nil
}

func (r *userRepo) setFollowedToCache(uuid string, followed []*biz.Follow) {
	ctx := context.Background()
	_, err := r.data.redisCli.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		z := make([]*redis.Z, 0, len(followed))
		for _, item := range followed {
			z = append(z, &redis.Z{
				Score:  float64(item.Update.Unix()),
				Member: item.Followed,
			})
		}
		pipe.ZAddNX(ctx, "followed_"+uuid, z...)
		pipe.Expire(ctx, "followed_"+uuid, time.Minute*30)
		return nil
	})
	if err != nil {
		r.log.Errorf("fail to set user followed to cache: uuid(%s), followed(%v), err(%v)", uuid, followed, err)
	}
}

func (r *userRepo) GetFollowedListCount(ctx context.Context, uuid string) (int32, error) {
	var count int64
	err := r.data.db.WithContext(ctx).Model(&Follow{}).Where("follow = ? and status = ?", uuid, 1).Count(&count).Error
	if err != nil {
		return 0, errors.Wrapf(err, fmt.Sprintf("fail to get followed list count from db: followed(%v)", uuid))
	}
	return int32(count), nil
}

func (r *userRepo) GetUserSearch(ctx context.Context, page int32, search string) ([]*biz.UserSearch, int32, error) {
	if page < 1 {
		page = 1
	}
	index := int64(page - 1)
	reply := make([]*biz.UserSearch, 0)

	var buf bytes.Buffer
	var body map[string]interface{}
	if search != "" {
		body = map[string]interface{}{
			"from":    index * 10,
			"size":    10,
			"_source": []string{"introduce"},
			"query": map[string]interface{}{
				"bool": map[string]interface{}{
					"must": []map[string]interface{}{
						{"multi_match": map[string]interface{}{
							"query":  search,
							"fields": []string{"username"},
						}},
					},
				},
			},
			"highlight": map[string]interface{}{
				"fields": map[string]interface{}{
					"username": map[string]interface{}{
						"pre_tags":      "<span style='color:red'>",
						"post_tags":     "</span>",
						"no_match_size": 100,
					},
				},
			},
		}
	} else {
		body = map[string]interface{}{
			"from":    index * 10,
			"size":    10,
			"_source": []string{"introduce"},
			"query": map[string]interface{}{
				"bool": map[string]interface{}{
					"must": []map[string]interface{}{
						{"match_all": map[string]interface{}{}},
					},
				},
			},
			"highlight": map[string]interface{}{
				"fields": map[string]interface{}{
					"username": map[string]interface{}{
						"pre_tags":      "<span style='color:red'>",
						"post_tags":     "</span>",
						"no_match_size": 100,
					},
				},
			},
		}
	}

	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		return nil, 0, errors.Wrapf(err, fmt.Sprintf("error encoding query: page(%v), search(%s)", page, search))
	}

	res, err := r.data.elasticSearch.es.Search(
		r.data.elasticSearch.es.Search.WithContext(ctx),
		r.data.elasticSearch.es.Search.WithIndex("user"),
		r.data.elasticSearch.es.Search.WithBody(&buf),
		r.data.elasticSearch.es.Search.WithTrackTotalHits(true),
		r.data.elasticSearch.es.Search.WithPretty(),
	)
	if err != nil {
		return nil, 0, errors.Wrapf(err, fmt.Sprintf("error getting response from es: page(%v), search(%s)", page, search))
	}

	if res.IsError() {
		var e map[string]interface{}
		if err := json.NewDecoder(res.Body).Decode(&e); err != nil {
			return nil, 0, errors.Wrapf(err, fmt.Sprintf("error parsing the response body: page(%v), search(%s)", page, search))
		} else {
			return nil, 0, errors.Errorf(fmt.Sprintf("error search user from  es: reason(%v), page(%v), search(%s)", e, page, search))
		}
	}

	result := map[string]interface{}{}
	if err := json.NewDecoder(res.Body).Decode(&result); err != nil {
		return nil, 0, errors.Wrapf(err, fmt.Sprintf("error parsing the response body: page(%v), search(%s)", page, search))
	}

	for _, hit := range result["hits"].(map[string]interface{})["hits"].([]interface{}) {
		user := &biz.UserSearch{
			Uuid:      hit.(map[string]interface{})["_id"].(string),
			Introduce: hit.(map[string]interface{})["_source"].(map[string]interface{})["introduce"].(string),
		}
		if username, ok := hit.(map[string]interface{})["highlight"].(map[string]interface{})["username"]; ok {
			user.Username = username.([]interface{})[0].(string)
		}
		reply = append(reply, user)
	}
	res.Body.Close()
	return reply, int32(result["hits"].(map[string]interface{})["total"].(map[string]interface{})["value"].(float64)), nil
}

func (r *userRepo) EditUserSearch(ctx context.Context, uuid string, profile *biz.ProfileUpdate) error {
	user := &biz.ProfileUpdateMap{
		Username:  profile.Username,
		Introduce: profile.Introduce,
	}
	body, err := user.MarshalJSON()
	if err != nil {
		return errors.Wrapf(err, fmt.Sprintf("error marshaling document: account(%s), uuid(%s)", profile.Username, uuid))
	}

	req := esapi.IndexRequest{
		Index:      "user",
		DocumentID: uuid,
		Body:       bytes.NewReader(body),
		Refresh:    "true",
	}
	res, err := req.Do(ctx, r.data.elasticSearch.es)
	if err != nil {
		return errors.Wrapf(err, fmt.Sprintf("error getting user search edit response: account(%s), uuid(%s)", profile.Username, uuid))
	}
	defer res.Body.Close()

	if res.IsError() {
		var e map[string]interface{}
		if err := json.NewDecoder(res.Body).Decode(&e); err != nil {
			return errors.Wrapf(err, fmt.Sprintf("error parsing the response body: account(%s), uuid(%s)", profile.Username, uuid))
		} else {
			return errors.Errorf(fmt.Sprintf("error indexing document to es: reason(%v), account(%s), uuid(%s)", e, profile.Username, uuid))
		}
	}
	return nil
}

func (r *userRepo) SetProfile(ctx context.Context, profile *biz.ProfileUpdate) error {
	updateTime, err := strconv.ParseInt(profile.Updated, 10, 64)
	if err != nil {
		return errors.Wrapf(err, fmt.Sprintf("fail to convert string to int64, update: %v", profile.Updated))
	}
	p := &Profile{}
	p.Updated = updateTime
	p.Uuid = profile.Uuid
	p.Username = profile.Username
	p.School = profile.School
	p.Company = profile.Company
	p.Job = profile.Job
	p.Homepage = profile.Homepage
	p.Introduce = profile.Introduce
	p.Gitee = profile.Gitee
	p.Github = profile.Github
	err = r.data.DB(ctx).Model(&Profile{}).Where("uuid = ?", profile.Uuid).Updates(p).Error
	if err != nil {
		return errors.Wrapf(err, fmt.Sprintf("fail to set profile: profile(%v)", profile))
	}
	r.updateProfileToCache(ctx, p, "profile_"+profile.Uuid)
	return nil
}

func (r *userRepo) SetProfileUpdate(ctx context.Context, profile *biz.ProfileUpdate, status int32) (*biz.ProfileUpdate, error) {
	pu := &ProfileUpdate{}
	pu.Updated = time.Now().Unix()
	pu.Username = profile.Username
	pu.School = profile.School
	pu.Company = profile.Company
	pu.Job = profile.Job
	pu.Homepage = profile.Homepage
	pu.Github = profile.Github
	pu.Gitee = profile.Gitee
	pu.Introduce = profile.Introduce
	pu.Status = status
	err := r.data.DB(ctx).Model(&ProfileUpdate{}).Select("updated", "username", "school", "company", "job", "homepage", "github", "gitee", "introduce", "status").Where("uuid = ?", profile.Uuid).Updates(pu).Error
	if err != nil {
		return nil, errors.Wrapf(err, fmt.Sprintf("fail to update a profile: profile(%v)", profile))
	}
	profile.Updated = strconv.FormatInt(pu.Updated, 10)
	return profile, nil
}

func (r *userRepo) SendProfileToMq(ctx context.Context, profile *biz.ProfileUpdate) error {
	data, err := profile.MarshalJSON()
	if err != nil {
		return err
	}
	msg := &primitive.Message{
		Topic: "matrix",
		Body:  data,
	}
	msg.WithKeys([]string{profile.Uuid})
	_, err = r.data.mqPro.producer.SendSync(ctx, msg)
	if err != nil {
		return errors.Wrapf(err, fmt.Sprintf("fail to send profile to mq: %v", err))
	}
	return nil
}

func (r *userRepo) SendImageIrregularToMq(ctx context.Context, review *biz.ImageReview) error {
	data, err := review.MarshalJSON()
	if err != nil {
		return err
	}
	msg := &primitive.Message{
		Topic: "matrix",
		Body:  data,
	}
	msg.WithKeys([]string{review.Uuid})
	_, err = r.data.mqPro.producer.SendSync(ctx, msg)
	if err != nil {
		return errors.Wrapf(err, fmt.Sprintf("fail to send picture review to mq: %v", err))
	}
	return nil
}

func (r *userRepo) SendUserStatisticToMq(ctx context.Context, uuid, userUuid, mode string) error {
	achievement := &biz.SendUserStatisticMap{
		Follow:   uuid,
		Followed: userUuid,
		Mode:     mode,
	}
	data, err := achievement.MarshalJSON()
	if err != nil {
		return err
	}
	msg := &primitive.Message{
		Topic: "matrix",
		Body:  data,
	}
	msg.WithKeys([]string{uuid})
	_, err = r.data.mqPro.producer.SendSync(ctx, msg)
	if err != nil {
		return errors.Wrapf(err, fmt.Sprintf("fail to send user statistic to mq: uuid(%s)", uuid))
	}
	return nil
}

func (r *userRepo) ModifyProfileUpdateStatus(ctx context.Context, uuid, update string) error {
	updateTime, err := strconv.ParseInt(update, 10, 64)
	if err != nil {
		return errors.Wrapf(err, fmt.Sprintf("fail to convert string to int64, update: %v", update))
	}
	err = r.data.DB(ctx).Model(&ProfileUpdate{}).Where("uuid = ? and updated = ?", uuid, updateTime).Update("status", 1).Error
	if err != nil {
		return errors.Wrapf(err, fmt.Sprintf("fail to modify profile update status: uuid(%v)", uuid))
	}
	return nil
}

func (r *userRepo) getProfileFromCache(ctx context.Context, key string) (*Profile, error) {
	result, err := r.data.redisCli.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return nil, kerrors.NotFound("profile not found from cache", fmt.Sprintf("key(%s)", key))
	}
	if err != nil {
		return nil, errors.Wrapf(err, fmt.Sprintf("fail to get profile from cache: redis.Get(%v)", key))
	}
	var cacheProfile = &Profile{}
	err = cacheProfile.UnmarshalJSON([]byte(result))
	if err != nil {
		return nil, errors.Wrapf(err, fmt.Sprintf("json unmarshal error: profile(%v)", result))
	}
	return cacheProfile, nil
}

func (r *userRepo) GetUserFollow(ctx context.Context, uuid, userUuid string) (bool, error) {
	f := &Follow{}
	err := r.data.db.WithContext(ctx).Select("id").Where("follow = ? and followed = ? and status = ?", uuid, userUuid, 1).First(f).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, errors.Wrapf(err, fmt.Sprintf("fail to get user follow from db: follow(%s), followed(%s)", uuid, userUuid))
	}
	return true, nil
}

func (r *userRepo) GetUserFollows(ctx context.Context, uuid string) ([]string, error) {
	follows, err := r.getUserFollowsFromCache(ctx, uuid)
	if err != nil {
		return nil, err
	}

	size := len(follows)
	if size != 0 {
		return follows, nil
	}

	follows, err = r.getUserFollowsFromDB(ctx, uuid)
	if err != nil {
		return nil, err
	}

	size = len(follows)
	if size != 0 {
		newCtx, _ := context.WithTimeout(context.Background(), time.Second*2)
		go r.data.Recover(newCtx, func(ctx context.Context) {
			r.setUserFollowsToCache(uuid, follows)
		})()
	}
	return follows, nil
}

func (r *userRepo) getUserFollowsFromCache(ctx context.Context, uuid string) ([]string, error) {
	follows, err := r.data.redisCli.SMembers(ctx, "user_follows_"+uuid).Result()
	if err != nil {
		return nil, errors.Wrapf(err, fmt.Sprintf("fail to get user follows from cache: uuid(%s)", uuid))
	}
	return follows, nil
}

func (r *userRepo) getUserFollowsFromDB(ctx context.Context, uuid string) ([]string, error) {
	list := make([]*Follow, 0)
	err := r.data.db.WithContext(ctx).Select("follow").Where("followed = ? and status = 1", uuid).Find(&list).Error
	if err != nil {
		return nil, errors.Wrapf(err, fmt.Sprintf("fail to get user follows from db: uuid(%s)", uuid))
	}

	follows := make([]string, 0, len(list))
	for _, item := range list {
		follows = append(follows, item.Follow)
	}
	return follows, nil
}

func (r *userRepo) GetAvatarReview(ctx context.Context, page int32, uuid string) ([]*biz.ImageReview, error) {
	key := "avatar_irregular_" + uuid
	review, err := r.getAvatarReviewFromCache(ctx, page, key)
	if err != nil {
		return nil, err
	}

	size := len(review)
	if size != 0 {
		return review, nil
	}

	review, err = r.getAvatarReviewFromDB(ctx, page, uuid)
	if err != nil {
		return nil, err
	}

	size = len(review)
	if size != 0 {
		newCtx, _ := context.WithTimeout(context.Background(), time.Second*2)
		go r.data.Recover(newCtx, func(ctx context.Context) {
			r.setAvatarReviewToCache(key, review)
		})()
	}
	return review, nil
}

func (r *userRepo) getAvatarReviewFromCache(ctx context.Context, page int32, key string) ([]*biz.ImageReview, error) {
	if page < 1 {
		page = 1
	}
	index := int64(page - 1)
	list, err := r.data.redisCli.LRange(ctx, key, index*20, index*20+19).Result()
	if err != nil {
		return nil, errors.Wrapf(err, fmt.Sprintf("fail to get avatar irregular list from cache: key(%s), page(%v)", key, page))
	}

	review := make([]*biz.ImageReview, 0, len(list))
	for _index, item := range list {
		var avatarReview = &biz.ImageReview{}
		err = avatarReview.UnmarshalJSON([]byte(item))
		if err != nil {
			return nil, errors.Wrapf(err, fmt.Sprintf("json unmarshal error: avatarReview(%v)", item))
		}
		review = append(review, &biz.ImageReview{
			Id:       int32(_index+1) + (page-1)*20,
			Uuid:     avatarReview.Uuid,
			CreateAt: avatarReview.CreateAt,
			JobId:    avatarReview.JobId,
			Url:      avatarReview.Url,
			Label:    avatarReview.Label,
			Result:   avatarReview.Result,
			Category: avatarReview.Category,
			SubLabel: avatarReview.SubLabel,
			Score:    avatarReview.Score,
		})
	}
	return review, nil
}

func (r *userRepo) getAvatarReviewFromDB(ctx context.Context, page int32, uuid string) ([]*biz.ImageReview, error) {
	if page < 1 {
		page = 1
	}
	index := int(page - 1)
	list := make([]*AvatarReview, 0)
	err := r.data.db.WithContext(ctx).Where("uuid", uuid).Order("id desc").Offset(index * 20).Limit(20).Find(&list).Error
	if err != nil {
		return nil, errors.Wrapf(err, fmt.Sprintf("fail to get avatar review from db: page(%v), uuid(%s)", page, uuid))
	}

	review := make([]*biz.ImageReview, 0, len(list))
	for _index, item := range list {
		review = append(review, &biz.ImageReview{
			Id:       int32(_index+1) + (page-1)*20,
			Uuid:     item.Uuid,
			JobId:    item.JobId,
			CreateAt: item.CreatedAt.Format("2006-01-02"),
			Url:      item.Url,
			Label:    item.Label,
			Result:   item.Result,
			Category: item.Category,
			SubLabel: item.SubLabel,
			Score:    item.Score,
		})
	}
	return review, nil
}

func (r *userRepo) setAvatarReviewToCache(key string, review []*biz.ImageReview) {
	ctx := context.Background()
	_, err := r.data.redisCli.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		list := make([]interface{}, 0, len(review))
		for _, item := range review {
			m, err := item.MarshalJSON()
			if err != nil {
				return errors.Wrapf(err, fmt.Sprintf("fail to marshal avatar review: avatarReview(%v)", review))
			}
			list = append(list, m)
		}
		pipe.RPush(ctx, key, list...)
		pipe.Expire(ctx, key, time.Minute*30)
		return nil
	})
	if err != nil {
		r.log.Errorf("fail to set avatar review to cache: avatarReview(%v), err(%v)", review, err)
	}
}

func (r *userRepo) SetAvatarIrregularToCache(ctx context.Context, review *biz.ImageReview) error {
	marshal, err := review.MarshalJSON()
	if err != nil {
		return errors.Wrapf(err, fmt.Sprintf("fail to set avatar irregular to json: json.Marshal(%v)", review))
	}
	keys := []string{"avatar_irregular_" + review.Uuid}
	values := []interface{}{marshal}
	_, err = r.data.redisCli.EvalSha(ctx, "8f6205011a2b264278a7c5bc0a2bcd1006ac6e5d", keys, values...).Result()
	if err != nil {
		return errors.Wrapf(err, fmt.Sprintf("fail to set avatar irregular to cache: review(%v)", review))
	}
	return nil
}

func (r *userRepo) SetAvatarIrregular(ctx context.Context, review *biz.ImageReview) (*biz.ImageReview, error) {
	ar := &AvatarReview{
		Uuid:     review.Uuid,
		JobId:    review.JobId,
		Url:      review.Url,
		Label:    review.Label,
		Result:   review.Result,
		Category: review.Category,
		SubLabel: review.SubLabel,
		Score:    review.Score,
	}
	err := r.data.DB(ctx).Select("Uuid", "JobId", "Url", "Label", "Result", "Category", "SubLabel", "Score").Create(ar).Error
	if err != nil {
		return nil, errors.Wrapf(err, fmt.Sprintf("fail to add avatar review record: review(%v)", review))
	}
	review.Id = int32(ar.ID)
	review.CreateAt = time.Now().Format("2006-01-02")
	return review, nil
}

func (r *userRepo) GetCoverReview(ctx context.Context, page int32, uuid string) ([]*biz.ImageReview, error) {
	key := "cover_irregular_" + uuid
	review, err := r.getCoverReviewFromCache(ctx, page, key)
	if err != nil {
		return nil, err
	}

	size := len(review)
	if size != 0 {
		return review, nil
	}

	review, err = r.getCoverReviewFromDB(ctx, page, uuid)
	if err != nil {
		return nil, err
	}

	size = len(review)
	if size != 0 {
		newCtx, _ := context.WithTimeout(context.Background(), time.Second*2)
		go r.data.Recover(newCtx, func(ctx context.Context) {
			r.setCoverReviewToCache(key, review)
		})()
	}
	return review, nil
}

func (r *userRepo) getCoverReviewFromCache(ctx context.Context, page int32, key string) ([]*biz.ImageReview, error) {
	if page < 1 {
		page = 1
	}
	index := int64(page - 1)
	list, err := r.data.redisCli.LRange(ctx, key, index*20, index*20+19).Result()
	if err != nil {
		return nil, errors.Wrapf(err, fmt.Sprintf("fail to get cover irregular list from cache: key(%s), page(%v)", key, page))
	}

	review := make([]*biz.ImageReview, 0, len(list))
	for _index, item := range list {
		var avatarReview = &biz.ImageReview{}
		err = avatarReview.UnmarshalJSON([]byte(item))
		if err != nil {
			return nil, errors.Wrapf(err, fmt.Sprintf("json unmarshal error: coverReview(%v)", item))
		}
		review = append(review, &biz.ImageReview{
			Id:       int32(_index+1) + (page-1)*20,
			Uuid:     avatarReview.Uuid,
			CreateAt: avatarReview.CreateAt,
			JobId:    avatarReview.JobId,
			Url:      avatarReview.Url,
			Label:    avatarReview.Label,
			Result:   avatarReview.Result,
			Category: avatarReview.Category,
			SubLabel: avatarReview.SubLabel,
			Score:    avatarReview.Score,
		})
	}
	return review, nil
}

func (r *userRepo) getCoverReviewFromDB(ctx context.Context, page int32, uuid string) ([]*biz.ImageReview, error) {
	if page < 1 {
		page = 1
	}
	index := int(page - 1)
	list := make([]*CoverReview, 0)
	err := r.data.db.WithContext(ctx).Where("uuid", uuid).Order("id desc").Offset(index * 20).Limit(20).Find(&list).Error
	if err != nil {
		return nil, errors.Wrapf(err, fmt.Sprintf("fail to get cover review from db: page(%v), uuid(%s)", page, uuid))
	}

	review := make([]*biz.ImageReview, 0, len(list))
	for _index, item := range list {
		review = append(review, &biz.ImageReview{
			Id:       int32(_index+1) + (page-1)*20,
			Uuid:     item.Uuid,
			JobId:    item.JobId,
			CreateAt: item.CreatedAt.Format("2006-01-02"),
			Url:      item.Url,
			Label:    item.Label,
			Result:   item.Result,
			Category: item.Category,
			SubLabel: item.SubLabel,
			Score:    item.Score,
		})
	}
	return review, nil
}

func (r *userRepo) setCoverReviewToCache(key string, review []*biz.ImageReview) {
	ctx := context.Background()
	_, err := r.data.redisCli.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		list := make([]interface{}, 0, len(review))
		for _, item := range review {
			m, err := item.MarshalJSON()
			if err != nil {
				return errors.Wrapf(err, fmt.Sprintf("fail to marshal cover review: coverReview(%v)", review))
			}
			list = append(list, m)
		}
		pipe.RPush(ctx, key, list...)
		pipe.Expire(ctx, key, time.Minute*30)
		return nil
	})
	if err != nil {
		r.log.Errorf("fail to set cover review to cache: coverReview(%v), err(%v)", review, err)
	}
}

func (r *userRepo) SetCoverIrregular(ctx context.Context, review *biz.ImageReview) (*biz.ImageReview, error) {
	ar := &CoverReview{
		Uuid:     review.Uuid,
		JobId:    review.JobId,
		Url:      review.Url,
		Label:    review.Label,
		Result:   review.Result,
		Category: review.Category,
		SubLabel: review.SubLabel,
		Score:    review.Score,
	}
	err := r.data.DB(ctx).Select("Uuid", "JobId", "Url", "Label", "Result", "Category", "SubLabel", "Score").Create(ar).Error
	if err != nil {
		return nil, errors.Wrapf(err, fmt.Sprintf("fail to add cover review record: review(%v)", review))
	}
	review.Id = int32(ar.ID)
	review.CreateAt = time.Now().Format("2006-01-02")
	return review, nil
}

func (r *userRepo) SetCoverIrregularToCache(ctx context.Context, review *biz.ImageReview) error {
	marshal, err := review.MarshalJSON()
	if err != nil {
		return errors.Wrapf(err, fmt.Sprintf("fail to set cover irregular to json: json.Marshal(%v)", review))
	}
	keys := []string{"cover_irregular_" + review.Uuid}
	values := []interface{}{marshal}
	_, err = r.data.redisCli.EvalSha(ctx, "8f6205011a2b264278a7c5bc0a2bcd1006ac6e5d", keys, values...).Result()
	if err != nil {
		return errors.Wrapf(err, fmt.Sprintf("fail to set cover irregular to cache: review(%v)", review))
	}
	return nil
}

func (r *userRepo) setUserFollowsToCache(uuid string, follows []string) {
	members := make([]interface{}, 0, len(follows))
	ctx := context.Background()
	for _, item := range follows {
		members = append(members, item)
	}
	_, err := r.data.redisCli.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.SAdd(ctx, "user_follows_"+uuid, members)
		pipe.Expire(ctx, "user_follows_"+uuid, time.Minute*30)
		return nil
	})
	if err != nil {
		r.log.Errorf("fail to set user follows to cache: uuid(%s), follows(%v), err(%v)", uuid, follows, err)
	}
}

func (r *userRepo) setProfileToCache(ctx context.Context, profile *Profile, key string) {
	marshal, err := profile.MarshalJSON()
	if err != nil {
		r.log.Errorf("fail to set user profile to json: json.Marshal(%v), error(%v)", profile, err)
		return
	}
	err = r.data.redisCli.SetNX(ctx, key, string(marshal), time.Minute*30).Err()
	if err != nil {
		r.log.Errorf("fail to set user profile to cache: redis.Set(%v), error(%v)", profile, err)
	}
}

func (r *userRepo) updateProfileToCache(ctx context.Context, profile *Profile, key string) {
	marshal, err := profile.MarshalJSON()
	if err != nil {
		r.log.Errorf("fail to set user profile to json: json.Marshal(%v), error(%v)", profile, err)
		return
	}
	err = r.data.redisCli.Set(ctx, key, string(marshal), time.Minute*30).Err()
	if err != nil {
		r.log.Errorf("fail to set user profile to cache: redis.Set(%v), error(%v)", profile, err)
	}
}

func (r *userRepo) SetUserEmail(ctx context.Context, id int64, email string) error {
	err := r.data.db.WithContext(ctx).Model(&User{}).Where("id = ?", id).Update("email", email).Error
	if err != nil {
		return errors.Wrapf(err, fmt.Sprintf("fail to set user email: user_id(%v), email(%s)", id, email))
	}
	return nil
}

func (r *userRepo) SetUserFollow(ctx context.Context, uuid, userUuid string) error {
	follow := &Follow{
		Follow:   uuid,
		Followed: userUuid,
		Status:   1,
	}
	err := r.data.DB(ctx).Clauses(clause.OnConflict{
		DoUpdates: clause.Assignments(map[string]interface{}{"status": 1}),
	}).Create(follow).Error
	if err != nil {
		return errors.Wrapf(err, fmt.Sprintf("fail to add a follow: follow(%s), followed(%s)", uuid, userUuid))
	}
	return nil
}

func (r *userRepo) SetUserFollowToCache(ctx context.Context, uuid, userUuid string) error {
	keys := []string{"user_follows_" + userUuid, "follow_" + userUuid, "followed_" + uuid}
	values := []interface{}{uuid, userUuid, float64(time.Now().Unix())}
	_, err := r.data.redisCli.EvalSha(ctx, "96859cb1c2a7f67b4320e5be8289eba8113e333f", keys, values...).Result()
	if err != nil {
		return errors.Wrapf(err, fmt.Sprintf("fail to set user follow to cache: uuid(%s), userUuid(%s)", uuid, userUuid))
	}
	return nil
}

func (r *userRepo) CancelUserFollow(ctx context.Context, uuid, userUuid string) error {
	f := &Follow{
		Status: 2,
	}
	err := r.data.DB(ctx).Model(&Follow{}).Where("follow = ? and followed = ?", uuid, userUuid).Updates(f).Error
	if err != nil {
		return errors.Wrapf(err, fmt.Sprintf("fail to cancel a follow: follow(%s), followed(%s)", uuid, userUuid))
	}
	return nil
}

func (r *userRepo) CancelUserFollowFromCache(ctx context.Context, uuid, userUuid string) error {
	_, err := r.data.redisCli.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.SRem(ctx, "user_follows_"+userUuid, uuid)
		pipe.ZRem(ctx, "follow_"+userUuid, uuid)
		pipe.ZRem(ctx, "followed_"+uuid, userUuid)
		return nil
	})
	if err != nil {
		return errors.Wrapf(err, fmt.Sprintf("fail to cancel a follow: follow(%s), followed(%s)", uuid, userUuid))
	}
	return nil
}

func (r *userRepo) SetFollowToMq(ctx context.Context, follow *biz.Follow, mode string) error {
	followMap := &biz.SetFollowMap{
		Uuid:   follow.Follow,
		UserId: follow.Followed,
		Mode:   mode,
	}
	data, err := followMap.MarshalJSON()
	if err != nil {
		return err
	}
	msg := &primitive.Message{
		Topic: "matrix",
		Body:  data,
	}
	msg.WithKeys([]string{follow.Followed})
	_, err = r.data.mqPro.producer.SendSync(ctx, msg)
	if err != nil {
		return errors.Wrapf(err, fmt.Sprintf("fail to send follow to mq: %v", follow))
	}
	return nil
}
