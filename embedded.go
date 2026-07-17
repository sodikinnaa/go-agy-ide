package main

import _ "embed"

//go:embed index.html
var embeddedIndexHTML string

//go:embed login.html
var embeddedLoginHTML string

//go:embed login-pwd.html
var embeddedLoginPwdHTML string

//go:embed manifest.json
var embeddedManifestJSON string

//go:embed sw.js
var embeddedServiceWorkerJS string

//go:embed icon-192.png
var embeddedIcon192 []byte

//go:embed icon-512.png
var embeddedIcon512 []byte
