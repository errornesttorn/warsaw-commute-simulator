package main

import (
	"fmt"

	rl "github.com/gen2brain/raylib-go/raylib"
)

const (
	modelAmbientStrength = float32(1.62)
	modelSunStrength     = float32(1.58)
	modelExposure        = float32(2.14)
)

type modelLightingResources struct {
	Shader           rl.Shader
	Valid            bool
	SunDirLoc        int32
	AmbientLoc       int32
	SunStrengthLoc   int32
	ExposureLoc      int32
	WarnedAssignFail bool
}

var modelLighting modelLightingResources

func initModelLightingShader() {
	shader := rl.LoadShaderFromMemory(modelLightingVertexShader, modelLightingFragmentShader)
	if !rl.IsShaderValid(shader) {
		fmt.Println("Model lighting shader unavailable; props and vehicle models will use imported materials")
		modelLighting = modelLightingResources{}
		return
	}
	shader.UpdateLocation(int32(rl.ShaderLocVertexPosition), rl.GetShaderLocationAttrib(shader, "vertexPosition"))
	shader.UpdateLocation(int32(rl.ShaderLocVertexTexcoord01), rl.GetShaderLocationAttrib(shader, "vertexTexCoord"))
	shader.UpdateLocation(int32(rl.ShaderLocVertexNormal), rl.GetShaderLocationAttrib(shader, "vertexNormal"))
	shader.UpdateLocation(int32(rl.ShaderLocMatrixMvp), rl.GetShaderLocation(shader, "mvp"))
	shader.UpdateLocation(int32(rl.ShaderLocMatrixModel), rl.GetShaderLocation(shader, "matModel"))
	shader.UpdateLocation(int32(rl.ShaderLocMatrixNormal), rl.GetShaderLocation(shader, "matNormal"))
	shader.UpdateLocation(int32(rl.ShaderLocMapAlbedo), rl.GetShaderLocation(shader, "texture0"))
	shader.UpdateLocation(int32(rl.ShaderLocColorDiffuse), rl.GetShaderLocation(shader, "colDiffuse"))

	modelLighting = modelLightingResources{
		Shader:         shader,
		Valid:          true,
		SunDirLoc:      rl.GetShaderLocation(shader, "sunDir"),
		AmbientLoc:     rl.GetShaderLocation(shader, "ambientStrength"),
		SunStrengthLoc: rl.GetShaderLocation(shader, "sunStrength"),
		ExposureLoc:    rl.GetShaderLocation(shader, "exposure"),
	}
	updateModelLightingShaderUniforms()
}

func updateModelLightingShaderUniforms() {
	if !modelLighting.Valid {
		return
	}
	setShaderVec3(modelLighting.Shader, modelLighting.SunDirLoc, normalizeVec3(rl.NewVector3(-0.35, 0.82, 0.45)))
	setShaderFloat(modelLighting.Shader, modelLighting.AmbientLoc, modelAmbientStrength)
	setShaderFloat(modelLighting.Shader, modelLighting.SunStrengthLoc, modelSunStrength)
	setShaderFloat(modelLighting.Shader, modelLighting.ExposureLoc, modelExposure)
}

func unloadModelLightingShader() {
	if modelLighting.Valid {
		rl.UnloadShader(modelLighting.Shader)
	}
	modelLighting = modelLightingResources{}
}

func applyModelLightingToModel(model *rl.Model) {
	if model == nil || !modelLighting.Valid {
		return
	}
	materials := model.GetMaterials()
	if len(materials) == 0 {
		return
	}
	for i := range materials {
		materials[i].Shader = modelLighting.Shader
	}
}

func setShaderFloat(shader rl.Shader, loc int32, value float32) {
	if loc < 0 {
		return
	}
	rl.SetShaderValue(shader, loc, []float32{value}, rl.ShaderUniformFloat)
}

func setShaderVec3(shader rl.Shader, loc int32, value rl.Vector3) {
	if loc < 0 {
		return
	}
	rl.SetShaderValue(shader, loc, []float32{value.X, value.Y, value.Z}, rl.ShaderUniformVec3)
}

const modelLightingVertexShader = `#version 330

in vec3 vertexPosition;
in vec2 vertexTexCoord;
in vec3 vertexNormal;

uniform mat4 mvp;
uniform mat4 matModel;
uniform mat4 matNormal;

out vec2 fragTexCoord;
out vec3 fragNormal;

void main()
{
    fragTexCoord = vertexTexCoord;
    fragNormal = normalize(vec3(matNormal*vec4(vertexNormal, 0.0)));
    gl_Position = mvp*vec4(vertexPosition, 1.0);
}`

const modelLightingFragmentShader = `#version 330

in vec2 fragTexCoord;
in vec3 fragNormal;

uniform sampler2D texture0;
uniform vec4 colDiffuse;
uniform vec3 sunDir;
uniform float ambientStrength;
uniform float sunStrength;
uniform float exposure;

out vec4 finalColor;

void main()
{
    vec4 texel = texture(texture0, fragTexCoord);
    vec3 normal = normalize(fragNormal);
    float diffuse = max(dot(normal, normalize(sunDir)), 0.0);
    float light = ambientStrength + diffuse*sunStrength;
    vec3 color = texel.rgb*colDiffuse.rgb*light*exposure;
    finalColor = vec4(color, texel.a*colDiffuse.a);
}`
