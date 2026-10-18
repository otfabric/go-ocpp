// SPDX-License-Identifier: MIT

package main

// Which role sends each action. The schemas do not say; the specifications
// do: OCPP 1.6 §4 and §5 (and the Security Whitepaper Ed. 4 §4–§6 for its
// extensions), and the "PDU sent by the … to the …" sentences and use
// cases of OCPP 2.0.1 Edition 4 Part 2. Part 2 once says CustomerInformation
// is "sent by the Charging Station to the CSMS"; its use case (N09) and the
// response sent back by the Charging Station make it a CSMS request, as here.
const (
	fromStation = "station"
	fromCSMS    = "csms"
	fromBoth    = "both"
)

var directions = map[string]map[string]string{
	"1.6": {
		// Core, from the Charge Point.
		"Authorize": fromStation, "BootNotification": fromStation, "DataTransfer": fromBoth,
		"DiagnosticsStatusNotification": fromStation, "FirmwareStatusNotification": fromStation,
		"Heartbeat": fromStation, "MeterValues": fromStation, "StartTransaction": fromStation,
		"StatusNotification": fromStation, "StopTransaction": fromStation,
		// Core, from the Central System.
		"CancelReservation": fromCSMS, "ChangeAvailability": fromCSMS, "ChangeConfiguration": fromCSMS,
		"ClearCache": fromCSMS, "ClearChargingProfile": fromCSMS, "GetCompositeSchedule": fromCSMS,
		"GetConfiguration": fromCSMS, "GetDiagnostics": fromCSMS, "GetLocalListVersion": fromCSMS,
		"RemoteStartTransaction": fromCSMS, "RemoteStopTransaction": fromCSMS, "ReserveNow": fromCSMS,
		"Reset": fromCSMS, "SendLocalList": fromCSMS, "SetChargingProfile": fromCSMS,
		"TriggerMessage": fromCSMS, "UnlockConnector": fromCSMS, "UpdateFirmware": fromCSMS,
		// Security Whitepaper extensions.
		"LogStatusNotification": fromStation, "SecurityEventNotification": fromStation,
		"SignCertificate": fromStation, "SignedFirmwareStatusNotification": fromStation,
		"CertificateSigned": fromCSMS, "DeleteCertificate": fromCSMS, "ExtendedTriggerMessage": fromCSMS,
		"GetInstalledCertificateIds": fromCSMS, "GetLog": fromCSMS, "InstallCertificate": fromCSMS,
		"SignedUpdateFirmware": fromCSMS,
	},
	"2.0.1": {
		"Authorize": fromStation, "BootNotification": fromStation, "ClearedChargingLimit": fromStation,
		"DataTransfer": fromBoth, "FirmwareStatusNotification": fromStation,
		"Get15118EVCertificate": fromStation, "GetCertificateStatus": fromStation, "Heartbeat": fromStation,
		"LogStatusNotification": fromStation, "MeterValues": fromStation, "NotifyChargingLimit": fromStation,
		"NotifyCustomerInformation": fromStation, "NotifyDisplayMessages": fromStation,
		"NotifyEVChargingNeeds": fromStation, "NotifyEVChargingSchedule": fromStation, "NotifyEvent": fromStation,
		"NotifyMonitoringReport": fromStation, "NotifyReport": fromStation,
		"PublishFirmwareStatusNotification": fromStation, "ReportChargingProfiles": fromStation,
		"ReservationStatusUpdate": fromStation, "SecurityEventNotification": fromStation,
		"SignCertificate": fromStation, "StatusNotification": fromStation, "TransactionEvent": fromStation,

		"CancelReservation": fromCSMS, "CertificateSigned": fromCSMS, "ChangeAvailability": fromCSMS,
		"ClearCache": fromCSMS, "ClearChargingProfile": fromCSMS, "ClearDisplayMessage": fromCSMS,
		"ClearVariableMonitoring": fromCSMS, "CostUpdated": fromCSMS, "CustomerInformation": fromCSMS,
		"DeleteCertificate": fromCSMS, "GetBaseReport": fromCSMS, "GetChargingProfiles": fromCSMS,
		"GetCompositeSchedule": fromCSMS, "GetDisplayMessages": fromCSMS, "GetInstalledCertificateIds": fromCSMS,
		"GetLocalListVersion": fromCSMS, "GetLog": fromCSMS, "GetMonitoringReport": fromCSMS,
		"GetReport": fromCSMS, "GetTransactionStatus": fromCSMS, "GetVariables": fromCSMS,
		"InstallCertificate": fromCSMS, "PublishFirmware": fromCSMS, "RequestStartTransaction": fromCSMS,
		"RequestStopTransaction": fromCSMS, "ReserveNow": fromCSMS, "Reset": fromCSMS,
		"SendLocalList": fromCSMS, "SetChargingProfile": fromCSMS, "SetDisplayMessage": fromCSMS,
		"SetMonitoringBase": fromCSMS, "SetMonitoringLevel": fromCSMS, "SetNetworkProfile": fromCSMS,
		"SetVariableMonitoring": fromCSMS, "SetVariables": fromCSMS, "TriggerMessage": fromCSMS,
		"UnlockConnector": fromCSMS, "UnpublishFirmware": fromCSMS, "UpdateFirmware": fromCSMS,
	},
}
