package controller

import (
	"strconv"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/web/authz"
	"github.com/mhsanaei/3x-ui/v3/internal/web/entity"
	"github.com/mhsanaei/3x-ui/v3/internal/web/middleware"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"

	"github.com/gin-gonic/gin"
)

// HostController scopes every route to the caller's rows: a reseller reads and
// writes only the host rows on inbounds they own (service/host_owner_scope.go).
type HostController struct {
	hostService   service.HostService
	clientService service.ClientService
	xrayService   service.XrayService
}

// reloadIfExit asks Xray to reload when an edit touched a host that leaves
// through an exit: those hosts are routing rules, the rest only feed links.
func (a *HostController) reloadIfExit(touched bool) {
	if touched {
		a.xrayService.SetToNeedRestart()
	}
}

func NewHostController(g *gin.RouterGroup) *HostController {
	a := &HostController{}
	a.initRouter(g)
	return a
}

func (a *HostController) initRouter(g *gin.RouterGroup) {
	g.GET("/list", a.list)
	g.GET("/raw", a.raw)
	g.GET("/get/:groupId", a.get)
	g.GET("/byInbound/:inboundId", a.byInbound)
	g.GET("/tags", a.tags)

	g.POST("/add", a.add)
	g.POST("/update/:groupId", a.update)
	g.POST("/del/:groupId", a.del)
	g.POST("/setEnable/:groupId", a.setEnable)
	g.POST("/reorder", a.reorder)
	g.POST("/bulk/add", a.add)
	g.POST("/bulk/setEnable", a.bulkSetEnable)
	g.POST("/bulk/del", a.bulkDel)
}

func (a *HostController) list(c *gin.Context) {
	hosts, err := a.hostService.GetHostsForOwner(ownerScopeOf(c))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.hosts.toasts.list"), err)
		return
	}
	jsonObj(c, hosts, nil)
}

// raw lists the caller's host rows with their numeric ids for id-based pickers
// such as the per-client host rule override.
func (a *HostController) raw(c *gin.Context) {
	hosts, err := a.hostService.GetRawHostsForOwner(ownerScopeOf(c))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.hosts.toasts.list"), err)
		return
	}
	jsonObj(c, hosts, nil)
}

func (a *HostController) get(c *gin.Context) {
	groupId := c.Param("groupId")
	h, err := a.hostService.GetHostGroupForOwner(groupId, ownerScopeOf(c))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.hosts.toasts.obtain"), err)
		return
	}
	jsonObj(c, h, nil)
}

func (a *HostController) byInbound(c *gin.Context) {
	inboundId, err := strconv.Atoi(c.Param("inboundId"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "get"), err)
		return
	}
	hosts, err := a.hostService.GetHostsByInboundForOwner(inboundId, ownerScopeOf(c))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.hosts.toasts.list"), err)
		return
	}
	jsonObj(c, hosts, nil)
}

func (a *HostController) tags(c *gin.Context) {
	tags, err := a.hostService.GetAllTagsForOwner(ownerScopeOf(c))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.hosts.toasts.list"), err)
		return
	}
	jsonObj(c, tags, nil)
}

// refuseExitHost: an exit host sends customers out through another server, and
// its inbounds, route and exit together say whose; all of that is the relays switch's.
func refuseExitHost(c *gin.Context, req *entity.HostGroup) bool {
	if strings.TrimSpace(req.Exit) == "" {
		return false
	}
	return refuseWithout(c, authz.SecRelays)
}

func (a *HostController) add(c *gin.Context) {
	req, ok := middleware.BindJSONAndValidate[entity.HostGroup](c)
	if !ok {
		return
	}
	if refuseExitHost(c, req) {
		return
	}
	if denyForeignTargets(c, &a.clientService, req.InboundIds, nil) {
		return
	}
	created, err := a.hostService.AddHostGroupForOwner(req, ownerScopeOf(c))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.hosts.toasts.add"), err)
		return
	}
	a.reloadIfExit(req.Exit != "")
	jsonMsgObj(c, I18nWeb(c, "pages.hosts.toasts.add"), created, nil)
}

func (a *HostController) update(c *gin.Context) {
	groupId := c.Param("groupId")
	req, ok := middleware.BindJSONAndValidate[entity.HostGroup](c)
	if !ok {
		return
	}
	if refuseExitHost(c, req) {
		return
	}
	if denyForeignTargets(c, &a.clientService, req.InboundIds, nil) {
		return
	}
	hadExit := a.hostService.HostsUseExit(groupId)
	updated, err := a.hostService.UpdateHostGroupForOwner(groupId, req, ownerScopeOf(c))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.hosts.toasts.update"), err)
		return
	}
	a.reloadIfExit(hadExit || req.Exit != "")
	jsonMsgObj(c, I18nWeb(c, "pages.hosts.toasts.update"), updated, nil)
}

func (a *HostController) del(c *gin.Context) {
	groupId := c.Param("groupId")
	hadExit := a.hostService.HostsUseExit(groupId)
	if err := a.hostService.DeleteHostGroupForOwner(groupId, ownerScopeOf(c)); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.hosts.toasts.delete"), err)
		return
	}
	a.reloadIfExit(hadExit)
	jsonMsg(c, I18nWeb(c, "pages.hosts.toasts.delete"), nil)
}

func (a *HostController) setEnable(c *gin.Context) {
	groupId := c.Param("groupId")
	body := struct {
		Enable bool `json:"enable" form:"enable"`
	}{}
	if err := c.ShouldBind(&body); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.hosts.toasts.update"), err)
		return
	}
	if err := a.hostService.SetHostGroupEnableForOwner(groupId, body.Enable, ownerScopeOf(c)); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.hosts.toasts.update"), err)
		return
	}
	a.reloadIfExit(a.hostService.HostsUseExit(groupId))
	jsonMsg(c, I18nWeb(c, "pages.hosts.toasts.update"), nil)
}

func (a *HostController) reorder(c *gin.Context) {
	var req struct {
		Ids []string `json:"ids" form:"ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.hosts.toasts.update"), err)
		return
	}
	if err := a.hostService.ReorderHostGroupsForOwner(req.Ids, ownerScopeOf(c)); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.hosts.toasts.update"), err)
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.hosts.toasts.update"), nil)
}

func (a *HostController) bulkSetEnable(c *gin.Context) {
	var req struct {
		Ids    []string `json:"ids" form:"ids"`
		Enable bool     `json:"enable" form:"enable"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.hosts.toasts.update"), err)
		return
	}
	if err := a.hostService.SetHostsGroupEnableForOwner(req.Ids, req.Enable, ownerScopeOf(c)); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.hosts.toasts.update"), err)
		return
	}
	a.reloadIfExit(a.hostService.HostsUseExit(req.Ids...))
	jsonMsg(c, I18nWeb(c, "pages.hosts.toasts.update"), nil)
}

func (a *HostController) bulkDel(c *gin.Context) {
	var req struct {
		Ids []string `json:"ids" form:"ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.hosts.toasts.delete"), err)
		return
	}
	hadExit := a.hostService.HostsUseExit(req.Ids...)
	if err := a.hostService.DeleteHostsGroupForOwner(req.Ids, ownerScopeOf(c)); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.hosts.toasts.delete"), err)
		return
	}
	a.reloadIfExit(hadExit)
	jsonMsg(c, I18nWeb(c, "pages.hosts.toasts.delete"), nil)
}
