// SPDX-License-Identifier: MIT

package main

import (
	"strings"
	"unicode"
)

// initialisms are written in capitals in Go identifiers, as Go style asks.
var initialisms = map[string]string{
	"id": "ID", "ids": "IDs", "url": "URL", "uri": "URI", "evse": "EVSE", "ev": "EV", "csms": "CSMS",
	"ocpp": "OCPP", "iccid": "ICCID", "imsi": "IMSI", "vpn": "VPN", "apn": "APN", "tls": "TLS",
	"ac": "AC", "dc": "DC", "der": "DER", "soc": "SoC", "ocsp": "OCSP", "http": "HTTP", "https": "HTTPS",
	"ip": "IP", "json": "JSON", "uuid": "UUID",
}

// words splits a camelCase or PascalCase name, or a value like
// "Energy.Active.Import.Register", into words.
func words(s string) []string {
	var out []string
	var cur []rune
	rs := []rune(s)
	flush := func() {
		if len(cur) > 0 {
			out = append(out, string(cur))
			cur = nil
		}
	}
	for i, r := range rs {
		switch {
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
			flush()
		case unicode.IsUpper(r) && len(cur) > 0 &&
			(unicode.IsLower(cur[len(cur)-1]) || unicode.IsDigit(cur[len(cur)-1]) ||
				(i+1 < len(rs) && unicode.IsLower(rs[i+1]))):
			flush()
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
	}
	flush()
	return out
}

// goName turns a JSON name into an exported Go identifier with Go's
// initialisms: "idTag" → "IDTag", "evseId" → "EVSEID".
func goName(s string) string {
	var b strings.Builder
	for _, w := range words(s) {
		if ini, ok := initialisms[strings.ToLower(w)]; ok {
			b.WriteString(ini)
			continue
		}
		r := []rune(w)
		r[0] = unicode.ToUpper(r[0])
		b.WriteString(string(r))
	}
	name := b.String()
	if name == "" || unicode.IsDigit([]rune(name)[0]) {
		name = "V" + name
	}
	return name
}

// typeNames is the set of generated type names, which constName consults.
var typeNames = map[string]bool{}

// constName is the name of an enumeration constant: the type name without
// its "EnumType" or "Type" suffix, then the value. When the shortened name
// is itself a type (OCPP 1.6 has TriggerMessageStatus and, in the security
// extensions, TriggerMessageStatusEnumType), only "Type" is dropped.
func constName(typeName, value string) string {
	base := strings.TrimSuffix(typeName, "EnumType")
	if base == typeName {
		base = strings.TrimSuffix(typeName, "Type")
	} else if typeNames[base] {
		base = strings.TrimSuffix(typeName, "Type")
	}
	return base + goName(value)
}

// names16 names the objects and enumerations that the OCPP 1.6 core
// schemas define inline, after the types of OCPP 1.6 chapter 7. The key is
// "<schema file>#<path>", with "[]" for array items. Identical definitions
// with the same name become one type; a schema location missing here is a
// generator error, so that no type is named by accident.
var names16 = map[string]string{
	"AuthorizeResponse#/idTagInfo":                                                       "IDTagInfo",
	"AuthorizeResponse#/idTagInfo/status":                                                "AuthorizationStatus",
	"SendLocalList#/localAuthorizationList/[]":                                           "AuthorizationData",
	"SendLocalList#/localAuthorizationList/[]/idTagInfo":                                 "IDTagInfo",
	"SendLocalList#/localAuthorizationList/[]/idTagInfo/status":                          "AuthorizationStatus",
	"StartTransactionResponse#/idTagInfo":                                                "IDTagInfo",
	"StartTransactionResponse#/idTagInfo/status":                                         "AuthorizationStatus",
	"StopTransactionResponse#/idTagInfo":                                                 "IDTagInfo",
	"StopTransactionResponse#/idTagInfo/status":                                          "AuthorizationStatus",
	"BootNotificationResponse#/status":                                                   "RegistrationStatus",
	"CancelReservationResponse#/status":                                                  "CancelReservationStatus",
	"ClearCacheResponse#/status":                                                         "ClearCacheStatus",
	"GetCompositeScheduleResponse#/status":                                               "GetCompositeScheduleStatus",
	"RemoteStartTransactionResponse#/status":                                             "RemoteStartStopStatus",
	"RemoteStopTransactionResponse#/status":                                              "RemoteStartStopStatus",
	"ResetResponse#/status":                                                              "ResetStatus",
	"ChangeAvailability#/type":                                                           "AvailabilityType",
	"ChangeAvailabilityResponse#/status":                                                 "AvailabilityStatus",
	"ChangeConfigurationResponse#/status":                                                "ConfigurationStatus",
	"ClearChargingProfile#/chargingProfilePurpose":                                       "ChargingProfilePurposeType",
	"ClearChargingProfileResponse#/status":                                               "ClearChargingProfileStatus",
	"DataTransferResponse#/status":                                                       "DataTransferStatus",
	"DiagnosticsStatusNotification#/status":                                              "DiagnosticsStatus",
	"FirmwareStatusNotification#/status":                                                 "FirmwareStatus",
	"GetCompositeSchedule#/chargingRateUnit":                                             "ChargingRateUnitType",
	"GetCompositeScheduleResponse#/chargingSchedule":                                     "ChargingSchedule",
	"GetCompositeScheduleResponse#/chargingSchedule/chargingRateUnit":                    "ChargingRateUnitType",
	"GetCompositeScheduleResponse#/chargingSchedule/chargingSchedulePeriod/[]":           "ChargingSchedulePeriod",
	"GetConfigurationResponse#/configurationKey/[]":                                      "KeyValue",
	"MeterValues#/meterValue/[]":                                                         "MeterValue",
	"MeterValues#/meterValue/[]/sampledValue/[]":                                         "SampledValue",
	"MeterValues#/meterValue/[]/sampledValue/[]/context":                                 "ReadingContext",
	"MeterValues#/meterValue/[]/sampledValue/[]/format":                                  "ValueFormat",
	"MeterValues#/meterValue/[]/sampledValue/[]/measurand":                               "Measurand",
	"MeterValues#/meterValue/[]/sampledValue/[]/phase":                                   "Phase",
	"MeterValues#/meterValue/[]/sampledValue/[]/location":                                "Location",
	"MeterValues#/meterValue/[]/sampledValue/[]/unit":                                    "UnitOfMeasure",
	"StopTransaction#/transactionData/[]":                                                "MeterValue",
	"StopTransaction#/transactionData/[]/sampledValue/[]":                                "SampledValue",
	"StopTransaction#/transactionData/[]/sampledValue/[]/context":                        "ReadingContext",
	"StopTransaction#/transactionData/[]/sampledValue/[]/format":                         "ValueFormat",
	"StopTransaction#/transactionData/[]/sampledValue/[]/measurand":                      "Measurand",
	"StopTransaction#/transactionData/[]/sampledValue/[]/phase":                          "Phase",
	"StopTransaction#/transactionData/[]/sampledValue/[]/location":                       "Location",
	"StopTransaction#/transactionData/[]/sampledValue/[]/unit":                           "UnitOfMeasure",
	"StopTransaction#/reason":                                                            "Reason",
	"RemoteStartTransaction#/chargingProfile":                                            "ChargingProfile",
	"RemoteStartTransaction#/chargingProfile/chargingProfilePurpose":                     "ChargingProfilePurposeType",
	"RemoteStartTransaction#/chargingProfile/chargingProfileKind":                        "ChargingProfileKindType",
	"RemoteStartTransaction#/chargingProfile/recurrencyKind":                             "RecurrencyKindType",
	"RemoteStartTransaction#/chargingProfile/chargingSchedule":                           "ChargingSchedule",
	"RemoteStartTransaction#/chargingProfile/chargingSchedule/chargingRateUnit":          "ChargingRateUnitType",
	"RemoteStartTransaction#/chargingProfile/chargingSchedule/chargingSchedulePeriod/[]": "ChargingSchedulePeriod",
	"SetChargingProfile#/csChargingProfiles":                                             "ChargingProfile",
	"SetChargingProfile#/csChargingProfiles/chargingProfilePurpose":                      "ChargingProfilePurposeType",
	"SetChargingProfile#/csChargingProfiles/chargingProfileKind":                         "ChargingProfileKindType",
	"SetChargingProfile#/csChargingProfiles/recurrencyKind":                              "RecurrencyKindType",
	"SetChargingProfile#/csChargingProfiles/chargingSchedule":                            "ChargingSchedule",
	"SetChargingProfile#/csChargingProfiles/chargingSchedule/chargingRateUnit":           "ChargingRateUnitType",
	"SetChargingProfile#/csChargingProfiles/chargingSchedule/chargingSchedulePeriod/[]":  "ChargingSchedulePeriod",
	"ReserveNowResponse#/status":                                                         "ReservationStatus",
	"Reset#/type":                                                                        "ResetType",
	"SendLocalList#/updateType":                                                          "UpdateType",
	"SendLocalListResponse#/status":                                                      "UpdateStatus",
	"SetChargingProfileResponse#/status":                                                 "ChargingProfileStatus",
	"StatusNotification#/errorCode":                                                      "ChargePointErrorCode",
	"StatusNotification#/status":                                                         "ChargePointStatus",
	"TriggerMessage#/requestedMessage":                                                   "MessageTrigger",
	"TriggerMessageResponse#/status":                                                     "TriggerMessageStatus",
	"UnlockConnectorResponse#/status":                                                    "UnlockStatus",
}
