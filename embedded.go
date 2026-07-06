package main

import _ "embed"

//go:embed index.html
var embeddedIndexHTML string

//go:embed login.html
var embeddedLoginHTML string

//go:embed login-pwd.html
var embeddedLoginPwdHTML string
