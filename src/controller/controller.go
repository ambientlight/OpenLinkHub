package controller

// Package: controller
// Author: Nikola Jurkovic
// License: GPL-3.0 or later

import (
	"OpenLinkHub/src/audio"
	"OpenLinkHub/src/config"
	"OpenLinkHub/src/dashboard"
	"OpenLinkHub/src/devices"
	"OpenLinkHub/src/devices/lcd"
	"OpenLinkHub/src/display"
	"OpenLinkHub/src/gputint"
	"OpenLinkHub/src/inputmanager"
	"OpenLinkHub/src/keyboards"
	"OpenLinkHub/src/language"
	"OpenLinkHub/src/logger"
	"OpenLinkHub/src/macro"
	"OpenLinkHub/src/media"
	"OpenLinkHub/src/metrics"
	"OpenLinkHub/src/monitor"
	"OpenLinkHub/src/motherboards"
	"OpenLinkHub/src/rgb"
	"OpenLinkHub/src/scheduler"
	"OpenLinkHub/src/server"
	"OpenLinkHub/src/stats"
	"OpenLinkHub/src/systeminfo"
	"OpenLinkHub/src/temperatures"
	"OpenLinkHub/src/version"
)

// Start will start new controller session
func Start() {
	version.Init()    // Build info
	config.Init()     // Configuration
	logger.Init()     // Logger
	display.Init()    // Displays
	media.Init()      // Media client
	audio.Init()      // Audio
	dashboard.Init()  // Dashboard
	systeminfo.Init() // Build system info
	metrics.Init()    // Metrics
	rgb.Init()        // RGB
	if err := gputint.Start(config.GetConfig().GPUStorm, func(err error) {
		if err != nil {
			logger.Log(logger.Fields{"error": err}).Warn("GPU Storm telemetry unavailable; holding tint")
		} else {
			logger.Log(logger.Fields{}).Info("GPU Storm telemetry recovered")
		}
	}); err != nil {
		logger.Log(logger.Fields{"error": err}).Warn("GPU Storm disabled due to invalid configuration")
	}
	lcd.Init()          // LCD
	temperatures.Init() // Temperatures
	keyboards.Init()    // Keyboards
	inputmanager.Init() // Input Manager
	stats.Init()        // Statistics
	macro.Init()        // Macro
	motherboards.Init() // Motherboards
	devices.Init()      // Devices
	monitor.Init()      // Monitor
	language.Init()     // Language
	scheduler.Init()    // Scheduler
	server.Init()       // REST & WebUI
}

// Stop will stop device control
func Stop() {
	devices.Stop()      // Devices
	gputint.Stop()      // GPU lighting sampler
	inputmanager.Stop() // Cleanup virtual devices
	audio.StopAudio()   // Virtual Audio
	media.Stop()        // Media client
}
