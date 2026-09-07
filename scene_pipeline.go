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

var minimaxH3BackendWorkflowSpecs = map[string]backendWorkflowSpec{
	minimaxH3CharacterTurnaroundScene: {
		ImagePath: backendUndressAnimeImagePath,
		VideoPath: backendMinimaxH3MultiPath,
	},
}

func resolveBackendWorkflow(req AnimeVideoRequest) (backendWorkflowSpec, string, error) {
	imageScene := strings.TrimSpace(req.SceneName)
	requestedVideoScene := strings.TrimSpace(req.VideoSceneName)

	if spec, ok := tenErosBackendWorkflowSpecs[imageScene]; ok {
		if requestedVideoScene != "" && requestedVideoScene != imageScene {
			return backendWorkflowSpec{}, "", fmt.Errorf(
				"video_scene_name must equal scene_name %q for the one-to-one 10eros workflow",
				imageScene,
			)
		}
		if spec.RequiresTargetPath && strings.TrimSpace(req.TargetPath) == "" {
			return backendWorkflowSpec{}, "", fmt.Errorf("target_path is required for scene_name %q", imageScene)
		}
		return spec, imageScene, nil
	}

	if spec, ok := minimaxH3BackendWorkflowSpecs[imageScene]; ok {
		if requestedVideoScene != "" && requestedVideoScene != imageScene {
			return backendWorkflowSpec{}, "", fmt.Errorf(
				"video_scene_name must equal scene_name %q for the one-to-one minimax-h3 workflow",
				imageScene,
			)
		}
		return spec, imageScene, nil
	}

	if _, ok := tenErosBackendWorkflowSpecs[requestedVideoScene]; ok {
		return backendWorkflowSpec{}, "", fmt.Errorf(
			"scene_name and video_scene_name must match for the one-to-one 10eros workflow",
		)
	}
	if _, ok := minimaxH3BackendWorkflowSpecs[requestedVideoScene]; ok {
		return backendWorkflowSpec{}, "", fmt.Errorf(
			"scene_name and video_scene_name must match for the one-to-one minimax-h3 workflow",
		)
	}

	return defaultBackendWorkflowSpec, defaultString(requestedVideoScene, imageScene), nil
}
