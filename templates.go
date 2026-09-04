package main

import "embed"

//go:embed templates/*.html static/*
var templateFS embed.FS
