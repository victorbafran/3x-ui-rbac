package controller

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/authz"
	"github.com/mhsanaei/3x-ui/v3/internal/web/entity"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service/panel"
)

// AdminController manages admin accounts, their switches and their assigned
// inbounds. Everything but /me and /permissions is super_admin only.
type AdminController struct {
	adminService panel.AdminService
}

func NewAdminController(g *gin.RouterGroup) *AdminController {
	a := &AdminController{}
	a.initRouter(g)
	return a
}

func (a *AdminController) initRouter(g *gin.RouterGroup) {
	g.GET("/me", a.me)
	g.GET("/permissions", a.permissions)
	g.GET("/list", a.list)
	g.GET("/get/:id", a.get)
	g.POST("/add", a.add)
	g.POST("/update/:id", a.update)
	g.POST("/resetPassword/:id", a.resetPassword)
	g.POST("/del/:id", a.del)
	g.POST("/:id/inbounds", a.setInbounds)
}

type adminForm struct {
	Username     string          `json:"username"`
	Password     string          `json:"password"`
	Role         model.Role      `json:"role"`
	Enabled      *bool           `json:"enabled"`
	TrafficQuota int64           `json:"trafficQuota"`
	UsersLimit   int64           `json:"usersLimit"`
	ExpireAt     int64           `json:"expireAt"`
	Permissions  map[string]bool `json:"permissions"`
	InboundIds   []int           `json:"inboundIds"`
}

func (f adminForm) spec() panel.AdminSpec {
	enabled := true
	if f.Enabled != nil {
		enabled = *f.Enabled
	}
	return panel.AdminSpec{
		Username: f.Username, Password: f.Password, Role: f.Role, Enabled: enabled,
		TrafficQuota: f.TrafficQuota, UsersLimit: f.UsersLimit, ExpireAt: f.ExpireAt,
		Permissions: f.Permissions,
	}
}

type resetPasswordForm struct {
	Password string `json:"password"`
}

type assignInboundsForm struct {
	InboundIds []int `json:"inboundIds"`
}

// me tells the signed-in admin their effective switches, so the UI can hide
// what they may not open instead of showing doors that will not budge.
func (a *AdminController) me(c *gin.Context) {
	user := authz.Actor(c)
	if user == nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), nil)
		return
	}
	jsonObj(c, entity.AdminMe{
		Id: user.Id, Username: user.Username, Role: string(user.Role),
		Permissions: authz.Effective(user),
	}, nil)
}

func (a *AdminController) permissions(c *gin.Context) {
	templates := map[string]map[string]bool{}
	for _, role := range []model.Role{model.RoleSuperAdmin, model.RoleManager, model.RoleReseller, model.RoleReadOnly} {
		templates[string(role)] = authz.RoleDefaults(role)
	}
	jsonObj(c, entity.PermissionCatalog{Keys: authz.AllPermissions, Templates: templates}, nil)
}

func (a *AdminController) list(c *gin.Context) {
	admins, err := a.adminService.List()
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonObj(c, admins, nil)
}

func (a *AdminController) get(c *gin.Context) {
	id, ok := pathId(c)
	if !ok {
		return
	}
	v, err := a.adminService.Get(id)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonObj(c, v, nil)
}

func (a *AdminController) add(c *gin.Context) {
	var form adminForm
	if err := c.ShouldBindJSON(&form); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	created, err := a.adminService.Create(form.spec())
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	if len(form.InboundIds) > 0 {
		if err := a.adminService.SetAssignments(created.Id, form.InboundIds); err != nil {
			jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
			return
		}
		created.InboundIds = form.InboundIds
	}
	jsonObj(c, created, nil)
}

func (a *AdminController) update(c *gin.Context) {
	id, ok := pathId(c)
	if !ok {
		return
	}
	var form adminForm
	if err := c.ShouldBindJSON(&form); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	if err := a.adminService.Update(id, form.spec()); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	if form.InboundIds != nil {
		if err := a.adminService.SetAssignments(id, form.InboundIds); err != nil {
			jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
			return
		}
	}
	v, err := a.adminService.Get(id)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonObj(c, v, nil)
}

func (a *AdminController) resetPassword(c *gin.Context) {
	id, ok := pathId(c)
	if !ok {
		return
	}
	var form resetPasswordForm
	if err := c.ShouldBindJSON(&form); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	if err := a.adminService.ResetPassword(id, form.Password); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonObj(c, id, nil)
}

func (a *AdminController) del(c *gin.Context) {
	id, ok := pathId(c)
	if !ok {
		return
	}
	actor := authz.Actor(c)
	actorId := 0
	if actor != nil {
		actorId = actor.Id
	}
	if err := a.adminService.Delete(actorId, id); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonObj(c, id, nil)
}

func (a *AdminController) setInbounds(c *gin.Context) {
	id, ok := pathId(c)
	if !ok {
		return
	}
	var form assignInboundsForm
	if err := c.ShouldBindJSON(&form); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	if err := a.adminService.SetAssignments(id, form.InboundIds); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	ids, err := a.adminService.Assignments(id)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonObj(c, ids, nil)
}

func pathId(c *gin.Context) (int, bool) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return 0, false
	}
	return id, true
}
