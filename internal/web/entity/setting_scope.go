package entity

import "reflect"

// resellerVisibleSettings are the only settings a non-super_admin may read.
// The list is an allowlist on purpose: a field added upstream stays private
// until someone puts it here, rather than leaking the day it appears.
var resellerVisibleSettings = map[string]bool{
	// Display and formatting the panel shell needs to render at all.
	"PageSize": true, "ExpireDiff": true, "TrafficDiff": true,
	"Datepicker": true, "TimeLocation": true, "RemarkTemplate": true,
	"WebBasePath": true, "TwoFactorEnable": true,

	// Subscription display: a reseller hands these links to their own clients.
	"SubEnable": true, "SubJsonEnable": true, "SubClashEnable": true,
	"SubURI": true, "SubJsonURI": true, "SubClashURI": true,
	"SubPath": true, "SubJsonPath": true, "SubClashPath": true,
	"SubTitle": true, "SubSupportUrl": true, "SubUpdates": true,
	"SubEncrypt": true, "SubShowIdentityOnAllLinks": true,
	"SubProfileMode": true, "SubProfileUrl": true,
	"HappLinkEnable": true, "SubHappAutoDetect": true,
	"SubExpiredTemplate": true, "SubTrafficDepletedTemplate": true,
	"SubCalendarExpireInclusive": true,
}

// ForReseller copies only the allowlisted fields into a fresh view. Everything
// else, including the Has* secret flags, is left zero.
func (v *AllSettingView) ForReseller() *AllSettingView {
	out := &AllSettingView{}
	src := reflect.ValueOf(v.AllSetting)
	dst := reflect.ValueOf(&out.AllSetting).Elem()
	t := src.Type()
	for i := 0; i < t.NumField(); i++ {
		if resellerVisibleSettings[t.Field(i).Name] {
			dst.Field(i).Set(src.Field(i))
		}
	}
	return out
}
