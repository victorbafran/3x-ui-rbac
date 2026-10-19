package controller

import (
	"strconv"

	"github.com/mhsanaei/3x-ui/v3/internal/web/middleware"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"

	"github.com/gin-gonic/gin"
)

// Relays are the servers a country's traffic leaves through. Enrolling one
// hands the panel root on somebody else's machine, so these routes are
// registered super-admin only in the authz registry, like the node routes.
type RelayController struct {
	relayService service.RelayService
}

func NewRelayController(g *gin.RouterGroup) *RelayController {
	a := &RelayController{}
	a.initRouter(g)
	return a
}

func (a *RelayController) initRouter(g *gin.RouterGroup) {
	g.GET("/list", a.list)
	g.GET("/options", a.options)

	g.POST("/add", a.add)
	g.POST("/update/:id", a.update)
	g.POST("/del/:id", a.del)
	g.POST("/setEnable/:id", a.setEnable)
	g.POST("/probe/:id", a.probe)
}

func (a *RelayController) list(c *gin.Context) {
	rows, err := a.relayService.List()
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.relays.toasts.list"), err)
		return
	}
	jsonObj(c, rows, nil)
}

// options feeds the exit dropdown on a cisco inbound. It is a separate,
// smaller route because an inbound form has no business reading a relay's
// status history or its last error.
func (a *RelayController) options(c *gin.Context) {
	rows, err := a.relayService.Options()
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.relays.toasts.list"), err)
		return
	}
	jsonObj(c, rows, nil)
}

func (a *RelayController) add(c *gin.Context) {
	req, ok := middleware.BindJSONAndValidate[service.RelayRequest](c)
	if !ok {
		return
	}
	created, err := a.relayService.Add(*req)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.relays.toasts.add"), err)
		return
	}
	jsonMsgObj(c, I18nWeb(c, "pages.relays.toasts.add"), created, nil)
}

func (a *RelayController) update(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.relays.toasts.update"), err)
		return
	}
	req, ok := middleware.BindJSONAndValidate[service.RelayRequest](c)
	if !ok {
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.relays.toasts.update"), a.relayService.Update(id, *req))
}

func (a *RelayController) del(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.relays.toasts.delete"), err)
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.relays.toasts.delete"), a.relayService.Delete(id))
}

func (a *RelayController) setEnable(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.relays.toasts.update"), err)
		return
	}
	enable, err := strconv.ParseBool(c.DefaultPostForm("enable", c.Query("enable")))
	if err != nil {
		var body struct {
			Enable bool `json:"enable"`
		}
		if bindErr := c.ShouldBindJSON(&body); bindErr != nil {
			jsonMsg(c, I18nWeb(c, "pages.relays.toasts.update"), bindErr)
			return
		}
		enable = body.Enable
	}
	jsonMsg(c, I18nWeb(c, "pages.relays.toasts.update"), a.relayService.SetEnable(id, enable))
}

// probe asks the relay what the internet gives it, which is the only honest
// way to answer "will my customers be able to open Gemini from this country".
func (a *RelayController) probe(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.relays.toasts.probe"), err)
		return
	}
	reach, err := a.relayService.Probe(id)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.relays.toasts.probe"), err)
		return
	}
	jsonObj(c, reach, nil)
}
