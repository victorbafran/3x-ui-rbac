package service

import (
	"net"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

var resolveHost = net.DefaultResolver.LookupIPAddr

func (s *RelayService) refuseASecondFront(*model.Relay) error { return nil }
