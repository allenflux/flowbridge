package main

import (
	"fmt"
	"strings"
)

const (
	backendUndressAnimeImagePath = "/api/public/generate/undress/anime"
	backendQwenTwoImagePath      = "/api/public/generate/qwen/two-image"
	backendUndressAnimeVideoPath = "/api/public/generate/undress/anime/video"
	backendLTX8sVideoPath        = "/api/public/generate/videos/scenes/8s/ltx"
	backendMinimaxH3MultiPath    = "/api/public/generate/videos/scenes/minimax-h3/multi"

	minimaxH3CharacterTurnaroundScene = "character_turnaround_sheet_h3"
)

type backendWorkflowSpec struct {
	ImagePath          string
	VideoPath          string
	RequiresTargetPath bool
}

var defaultBackendWorkflowSpec = backendWorkflowSpec{
	ImagePath: backendUndressAnimeImagePath,
	VideoPath: backendUndressAnimeVideoPath,
}

var tenErosBackendWorkflowSpecs = map[string]backendWorkflowSpec{
	"gay_doggy_10eros": {
		ImagePath:          backendQwenTwoImagePath,
		VideoPath:          backendLTX8sVideoPath,
		RequiresTargetPath: true,
	},
	"gay_cumshot_10eros": {
		ImagePath: backendUndressAnimeImagePath,
		VideoPath: backendLTX8sVideoPath,
	},
	"gay_anal_creampie_10eros": {
		ImagePath: backendUndressAnimeImagePath,
		VideoPath: backendLTX8sVideoPath,
	},
	"lesbian_kiss_10eros": {
		ImagePath:          backendQwenTwoImagePath,
		VideoPath:          backendLTX8sVideoPath,
		RequiresTargetPath: true,
	},
}

func qwenLTXWorkflowSpec(requiresTargetPath bool) backendWorkflowSpec {
	return backendWorkflowSpec{
		ImagePath:          backendQwenTwoImagePath,
		VideoPath:          backendLTX8sVideoPath,
		RequiresTargetPath: requiresTargetPath,
	}
}

var tenErosBatch2BackendWorkflowSpecs = map[string]backendWorkflowSpec{
	"gay_oral_cumshot_10eros":    qwenLTXWorkflowSpec(true),
	"lesbian_cunnilingus_10eros": qwenLTXWorkflowSpec(true),
	"gay_bondage_10eros":         qwenLTXWorkflowSpec(true),
	"gay_crossdressing_10eros":   qwenLTXWorkflowSpec(false),
	"gay_butt_slap_10eros":       qwenLTXWorkflowSpec(false),
	"gay_kneeling_doggy_10eros":  qwenLTXWorkflowSpec(true),
	"lesbian_strap_on_10eros":    qwenLTXWorkflowSpec(true),
	"lesbian_doggy_10eros":       qwenLTXWorkflowSpec(true),
	"lesbian_cowgirl_10eros":     qwenLTXWorkflowSpec(true),
	"gay_bar_doggy_10ero":        qwenLTXWorkflowSpec(true),
}

var minimaxH3BackendWorkflowSpecs = map[string]backendWorkflowSpec{
	minimaxH3CharacterTurnaroundScene: {
		ImagePath: backendUndressAnimeImagePath,
		VideoPath: backendMinimaxH3MultiPath,
	},
}

func workflowTypeForScene(sceneName string) string {
	sceneName = strings.TrimSpace(sceneName)
	if _, ok := tenErosBackendWorkflowSpecs[sceneName]; ok {
		return WorkflowTenErosImageVideo
	}
	if _, ok := tenErosBatch2BackendWorkflowSpecs[sceneName]; ok {
		return WorkflowTenErosBatch2ImageVideo
	}
	if _, ok := minimaxH3BackendWorkflowSpecs[sceneName]; ok {
		return WorkflowMinimaxH3ImageVideo
	}
	return WorkflowAnimeUndressVideo
}

func resolveOneToOneBackendWorkflow(req AnimeVideoRequest, specs map[string]backendWorkflowSpec, label string) (backendWorkflowSpec, string, error) {
	imageScene := strings.TrimSpace(req.SceneName)
	requestedVideoScene := strings.TrimSpace(req.VideoSceneName)
	spec, ok := specs[imageScene]
	if !ok {
		return backendWorkflowSpec{}, "", fmt.Errorf("scene_name %q is not supported by the %s workflow", imageScene, label)
	}
	if requestedVideoScene != "" && requestedVideoScene != imageScene {
		return backendWorkflowSpec{}, "", fmt.Errorf(
			"video_scene_name must equal scene_name %q for the one-to-one %s workflow",
			imageScene,
			label,
		)
	}
	if spec.RequiresTargetPath && strings.TrimSpace(req.TargetPath) == "" {
		return backendWorkflowSpec{}, "", fmt.Errorf("target_path is required for scene_name %q", imageScene)
	}
	return spec, imageScene, nil
}

func resolveBackendWorkflowForType(workflowType string, req AnimeVideoRequest) (backendWorkflowSpec, string, error) {
	switch workflowType {
	case WorkflowTenErosImageVideo:
		return resolveOneToOneBackendWorkflow(req, tenErosBackendWorkflowSpecs, "10eros")
	case WorkflowTenErosBatch2ImageVideo:
		return resolveOneToOneBackendWorkflow(req, tenErosBatch2BackendWorkflowSpecs, "10eros batch-2")
	case WorkflowMinimaxH3ImageVideo:
		return resolveOneToOneBackendWorkflow(req, minimaxH3BackendWorkflowSpecs, "minimax-h3")
	case WorkflowAnimeUndressVideo:
		imageScene := strings.TrimSpace(req.SceneName)
		return defaultBackendWorkflowSpec, defaultString(strings.TrimSpace(req.VideoSceneName), imageScene), nil
	default:
		return backendWorkflowSpec{}, "", fmt.Errorf("unsupported workflow_type %q", workflowType)
	}
}

func resolveBackendWorkflow(req AnimeVideoRequest) (backendWorkflowSpec, string, error) {
	workflowType := workflowTypeForScene(req.SceneName)
	requestedVideoScene := strings.TrimSpace(req.VideoSceneName)
	if workflowType == WorkflowAnimeUndressVideo && requestedVideoScene != "" && workflowTypeForScene(requestedVideoScene) != WorkflowAnimeUndressVideo {
		return backendWorkflowSpec{}, "", fmt.Errorf(
			"scene_name and video_scene_name must not cross dedicated workflow routes",
		)
	}
	return resolveBackendWorkflowForType(workflowType, req)
}
