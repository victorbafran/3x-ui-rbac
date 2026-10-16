package panel

import (
	"errors"
	"strings"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/crypto"
)

// AdminService manages the panel's admin accounts. Its guards exist to stop a
// super_admin locking everyone out of their own panel.
type AdminService struct{}

var (
	ErrUnknownRole       = errors.New("unknown role")
	ErrUsernameTaken     = errors.New("username already exists")
	ErrEmptyUsername     = errors.New("username can not be empty")
	ErrShortPassword     = errors.New("password must be at least 8 characters")
	ErrDeleteSelf        = errors.New("an admin can not delete their own account")
	ErrLastSuperAdmin    = errors.New("the last super_admin must stay a super_admin and enabled")
	ErrAdminNotFound     = errors.New("admin not found")
	minAdminPasswordRune = 8
)

// AdminView is an admin without the password hash.
type AdminView struct {
	Id           int        `json:"id" example:"2"`
	Username     string     `json:"username" example:"agent"`
	Password     string     `json:"password,omitempty"` // always empty; kept so a leak is visible in tests
	Role         model.Role `json:"role" example:"reseller"`
	Enabled      bool       `json:"enabled" example:"true"`
	TrafficQuota int64      `json:"trafficQuota" example:"0"`
}

func viewOf(u model.User) AdminView {
	return AdminView{Id: u.Id, Username: u.Username, Role: u.Role, Enabled: u.Enabled, TrafficQuota: u.TrafficQuota}
}

func (s *AdminService) List() ([]AdminView, error) {
	var users []model.User
	if err := database.GetDB().Order("id ASC").Find(&users).Error; err != nil {
		return nil, err
	}
	out := make([]AdminView, 0, len(users))
	for _, u := range users {
		out = append(out, viewOf(u))
	}
	return out, nil
}

func (s *AdminService) Create(username, password string, role model.Role, quota int64) (*AdminView, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return nil, ErrEmptyUsername
	}
	if len([]rune(password)) < minAdminPasswordRune {
		return nil, ErrShortPassword
	}
	if !role.Valid() {
		return nil, ErrUnknownRole
	}
	db := database.GetDB()
	var clash int64
	if err := db.Model(&model.User{}).Where("username = ?", username).Count(&clash).Error; err != nil {
		return nil, err
	}
	if clash > 0 {
		return nil, ErrUsernameTaken
	}
	hash, err := crypto.HashPasswordAsBcrypt(password)
	if err != nil {
		return nil, err
	}
	u := model.User{Username: username, Password: hash, Role: role, Enabled: true, TrafficQuota: quota}
	if err := db.Create(&u).Error; err != nil {
		return nil, err
	}
	v := viewOf(u)
	return &v, nil
}

// Update changes role, enabled and quota. A password change goes through
// ResetPassword so the two cannot be confused in a form post.
func (s *AdminService) Update(id int, role model.Role, enabled bool, quota int64) error {
	if !role.Valid() {
		return ErrUnknownRole
	}
	target, err := s.load(id)
	if err != nil {
		return err
	}
	losingSuperAdmin := target.Role == model.RoleSuperAdmin && (role != model.RoleSuperAdmin || !enabled)
	if losingSuperAdmin {
		spare, err := s.otherUsableSuperAdmins(id)
		if err != nil {
			return err
		}
		if spare == 0 {
			return ErrLastSuperAdmin
		}
	}
	updates := map[string]any{"role": string(role), "enabled": enabled, "traffic_quota": quota}
	// Losing a role or being switched off must end sessions already open.
	if target.Role != role || target.Enabled != enabled {
		updates["login_epoch"] = gorm.Expr("login_epoch + 1")
	}
	return database.GetDB().Model(&model.User{}).Where("id = ?", id).Updates(updates).Error
}

func (s *AdminService) ResetPassword(id int, password string) error {
	if len([]rune(password)) < minAdminPasswordRune {
		return ErrShortPassword
	}
	if _, err := s.load(id); err != nil {
		return err
	}
	hash, err := crypto.HashPasswordAsBcrypt(password)
	if err != nil {
		return err
	}
	return database.GetDB().Model(&model.User{}).Where("id = ?", id).Updates(map[string]any{
		"password":    hash,
		"login_epoch": gorm.Expr("login_epoch + 1"),
	}).Error
}

func (s *AdminService) Delete(actorId, id int) error {
	if actorId == id {
		return ErrDeleteSelf
	}
	target, err := s.load(id)
	if err != nil {
		return err
	}
	if target.Role == model.RoleSuperAdmin {
		spare, err := s.otherUsableSuperAdmins(id)
		if err != nil {
			return err
		}
		if spare == 0 {
			return ErrLastSuperAdmin
		}
	}
	return database.GetDB().Where("id = ?", id).Delete(&model.User{}).Error
}

func (s *AdminService) load(id int) (*model.User, error) {
	var u model.User
	err := database.GetDB().Where("id = ?", id).First(&u).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrAdminNotFound
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// otherUsableSuperAdmins counts the enabled super_admins that are not id.
func (s *AdminService) otherUsableSuperAdmins(id int) (int64, error) {
	var n int64
	err := database.GetDB().Model(&model.User{}).
		Where("role = ? AND enabled = ? AND id <> ?", string(model.RoleSuperAdmin), true, id).
		Count(&n).Error
	return n, err
}
