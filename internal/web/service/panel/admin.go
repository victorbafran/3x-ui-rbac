package panel

import (
	"errors"
	"strings"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/crypto"
	"github.com/mhsanaei/3x-ui/v3/internal/web/authz"
	"github.com/mhsanaei/3x-ui/v3/internal/web/entity"
)

// AdminService manages the panel's admin accounts. Its guards exist to stop a
// super_admin locking everyone out of their own panel.
type AdminService struct{}

var (
	ErrUnknownRole    = errors.New("unknown role")
	ErrUsernameTaken  = errors.New("username already exists")
	ErrEmptyUsername  = errors.New("username can not be empty")
	ErrShortPassword  = errors.New("password must be at least 8 characters")
	ErrDeleteSelf     = errors.New("an admin can not delete their own account")
	ErrLastSuperAdmin = errors.New("the last super_admin must stay a super_admin and enabled")
	ErrAdminNotFound  = errors.New("admin not found")
)

const minAdminPasswordRunes = 8

// AdminSpec is everything the admins page sets on an account. Permissions are
// overrides on the role template; Password is read by Create only.
type AdminSpec struct {
	Username     string
	Password     string
	Role         model.Role
	Enabled      bool
	TrafficQuota int64
	UsersLimit   int64
	ExpireAt     int64
	Permissions  map[string]bool
}

func viewOf(u model.User, inboundIds []int) entity.AdminView {
	overrides, err := authz.ParseOverrides(u.Permissions)
	if err != nil {
		overrides = map[string]bool{}
	}
	if inboundIds == nil {
		inboundIds = []int{}
	}
	return entity.AdminView{
		Id: u.Id, Username: u.Username, Role: string(u.Role), Enabled: u.Enabled,
		TrafficQuota: u.TrafficQuota, UsersLimit: u.UsersLimit, ExpireAt: u.ExpireAt,
		DisabledReason: u.DisabledReason, Permissions: overrides, InboundIds: inboundIds,
	}
}

func (s *AdminService) List() ([]entity.AdminView, error) {
	db := database.GetDB()
	var users []model.User
	if err := db.Order("id ASC").Find(&users).Error; err != nil {
		return nil, err
	}
	var links []model.AdminInbound
	if err := db.Order("inbound_id ASC").Find(&links).Error; err != nil {
		return nil, err
	}
	byAdmin := make(map[int][]int, len(users))
	for _, l := range links {
		byAdmin[l.AdminId] = append(byAdmin[l.AdminId], l.InboundId)
	}
	usage, err := UsageByAdmin()
	if err != nil {
		return nil, err
	}
	out := make([]entity.AdminView, 0, len(users))
	for _, u := range users {
		v := viewOf(u, byAdmin[u.Id])
		if used, ok := usage[u.Id]; ok {
			v.UsedUp, v.UsedDown, v.ClientCount = used.Up, used.Down, used.Clients
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *AdminService) Get(id int) (*entity.AdminView, error) {
	u, err := s.load(id)
	if err != nil {
		return nil, err
	}
	ids, err := s.Assignments(id)
	if err != nil {
		return nil, err
	}
	v := viewOf(*u, ids)
	return &v, nil
}

func (s *AdminService) Create(spec AdminSpec) (*entity.AdminView, error) {
	username := strings.TrimSpace(spec.Username)
	if username == "" {
		return nil, ErrEmptyUsername
	}
	if len([]rune(spec.Password)) < minAdminPasswordRunes {
		return nil, ErrShortPassword
	}
	if !spec.Role.Valid() {
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
	hash, err := crypto.HashPasswordAsBcrypt(spec.Password)
	if err != nil {
		return nil, err
	}
	u := model.User{
		Username: username, Password: hash, Role: spec.Role, Enabled: true,
		TrafficQuota: spec.TrafficQuota, UsersLimit: spec.UsersLimit, ExpireAt: spec.ExpireAt,
		Permissions: authz.EncodeOverrides(spec.Permissions),
	}
	if err := db.Create(&u).Error; err != nil {
		return nil, err
	}
	v := viewOf(u, nil)
	return &v, nil
}

// Update changes role, status, limits and overrides. Username and password
// are left alone: a password change goes through ResetPassword so the two
// cannot be confused in one form post.
func (s *AdminService) Update(id int, spec AdminSpec) error {
	if !spec.Role.Valid() {
		return ErrUnknownRole
	}
	target, err := s.load(id)
	if err != nil {
		return err
	}
	losingSuperAdmin := target.Role == model.RoleSuperAdmin && (spec.Role != model.RoleSuperAdmin || !spec.Enabled)
	if losingSuperAdmin {
		spare, err := s.otherUsableSuperAdmins(id)
		if err != nil {
			return err
		}
		if spare == 0 {
			return ErrLastSuperAdmin
		}
	}
	updates := map[string]any{
		"role":          string(spec.Role),
		"enabled":       spec.Enabled,
		"traffic_quota": spec.TrafficQuota,
		"users_limit":   spec.UsersLimit,
		"expire_at":     spec.ExpireAt,
		"permissions":   authz.EncodeOverrides(spec.Permissions),
	}
	// Switching an admin back on clears the reason the job or a hand recorded.
	if spec.Enabled {
		updates["disabled_reason"] = ""
	}
	// Losing a role or being switched off must end sessions already open.
	if target.Role != spec.Role || target.Enabled != spec.Enabled {
		updates["login_epoch"] = gorm.Expr("login_epoch + 1")
	}
	return database.GetDB().Model(&model.User{}).Where("id = ?", id).Updates(updates).Error
}

func (s *AdminService) ResetPassword(id int, password string) error {
	if len([]rune(password)) < minAdminPasswordRunes {
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

// Delete removes the admin and their assignments. Inbounds they created stay
// and keep their user_id, so the super_admin can reassign them afterwards.
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
	return database.GetDB().Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("admin_id = ?", id).Delete(&model.AdminInbound{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", id).Delete(&model.User{}).Error
	})
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
