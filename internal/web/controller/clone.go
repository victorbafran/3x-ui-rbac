package controller

import (
	"errors"
	"strconv"

	"github.com/mhsanaei/3x-ui/v3/internal/web/global"
	"github.com/mhsanaei/3x-ui/v3/internal/web/middleware"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"

	"github.com/gin-gonic/gin"
)

// CloneController manages the copies of this panel on other servers. Adding one
// hands the panel root on a new machine, so the authz registry keeps it super-admin only.
type CloneController struct {
	cloneService service.PanelCloneService
}

func NewCloneController(g *gin.RouterGroup) *CloneController {
	a := &CloneController{}
	a.initRouter(g)
	return a
}

func (a *CloneController) initRouter(g *gin.RouterGroup) {
	g.GET("/list", a.list)
	g.POST("/add", a.add)
	g.POST("/sync/:id", a.sync)
	g.POST("/check/:id", a.check)
	g.POST("/promote/:id", a.promote)
	g.POST("/del/:id", a.del)
}

func (a *CloneController) list(c *gin.Context) {
	rows, err := a.cloneService.List()
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.clones.toasts.list"), err)
		return
	}
	jsonObj(c, rows, nil)
}

func (a *CloneController) add(c *gin.Context) {
	msg := I18nWeb(c, "pages.clones.toasts.add")
	req, ok := middleware.BindJSONAndValidate[service.PanelCloneRequest](c)
	if !ok {
		return
	}
	created, err := a.cloneService.Add(*req)
	if err != nil {
		jsonMsg(c, msg, cloneError(c, err))
		return
	}
	jsonMsgObj(c, msg, created, nil)
}

func (a *CloneController) sync(c *gin.Context) {
	a.byId(c, "pages.clones.toasts.sync", a.cloneService.Sync)
}

func (a *CloneController) promote(c *gin.Context) {
	a.byId(c, "pages.clones.toasts.promote", a.cloneService.Promote)
}

func (a *CloneController) del(c *gin.Context) {
	a.byId(c, "pages.clones.toasts.delete", a.cloneService.Delete)
}

func (a *CloneController) check(c *gin.Context) {
	msg := I18nWeb(c, "pages.clones.toasts.check")
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, msg, err)
		return
	}
	row, err := a.cloneService.Check(id)
	if err != nil {
		jsonMsg(c, msg, cloneError(c, err))
		return
	}
	jsonMsgObj(c, msg, row, nil)
}

// byId runs an action on the copy named in the path. Sync and promote answer
// once their job has started; the row's step carries it from there.
func (a *CloneController) byId(c *gin.Context, toast string, action func(id int) error) {
	msg := I18nWeb(c, toast)
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, msg, err)
		return
	}
	jsonMsg(c, msg, cloneError(c, action(id)))
}

// cloneError shows the standby refusal in the operator's language.
func cloneError(c *gin.Context, err error) error {
	if errors.Is(err, global.ErrStandby) {
		return errors.New(I18nWeb(c, "pages.standby.refused"))
	}
	return err
}
