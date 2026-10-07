// Copyright 2026 Rustam Assanov. All rights reserved.
// Use of this source code is governed by an MIT-style
// license that can be found in the LICENSE file.

package config

import (
	"slices"
	"strings"
)

// Role names a job a model does. The config says which model currently does
// it, so swapping a model is one edited line and every subcommand follows.
const (
	RoleFast    = "fast"
	RoleSmart   = "smart"
	RoleDeep    = "deep"
	RoleCode    = "code"
	RoleEmbed   = "embed"
	RoleASR     = "asr"
	RoleASRFast = "asr-fast"
)

// DefaultFast is the fallback when the config says nothing at all.
// Deliberately the smallest useful model, so a machine with no config still
// answers.
const DefaultFast = "unsloth/Qwen3-1.7B-UD-Q8_K_XL"

// Backend says which native runtime a model needs. The two have separate
// library bundles and separate model stores, and mixing them up is the classic
// mistake here: KRONK_LIB_PATH does not select whisper.cpp, and a whisper model
// is a GGML .bin rather than a GGUF.
type Backend int

const (
	BackendLlama   Backend = iota // GGUF text models, llama.cpp
	BackendWhisper                // GGML speech models, whisper.cpp
)

func (b Backend) String() string {
	if b == BackendWhisper {
		return "whisper"
	}
	return "llama"
}

// Role binds a name the subcommands ask for to the config key that answers it
// and the default used when the config says nothing.
type Role struct {
	Name    string
	Key     string
	Backend Backend
	Default string
}

// Roles is the whole table, in the order status lists them.
var Roles = []Role{
	{RoleFast, "AI_MODEL_FAST", BackendLlama, DefaultFast},
	{RoleSmart, "AI_MODEL_SMART", BackendLlama, ""},
	{RoleDeep, "AI_MODEL_DEEP", BackendLlama, ""},
	{RoleCode, "AI_MODEL_CODE", BackendLlama, ""},
	{RoleEmbed, "AI_MODEL_EMBED", BackendLlama, ""},
	{RoleASR, "AI_ASR_MODEL", BackendWhisper, "large-v3-turbo"},
	{RoleASRFast, "AI_ASR_MODEL_FAST", BackendWhisper, "base"},
}

// fallbacks is what a role falls back to when nothing binds it.
//
// Having this in one table is the point of the package. The old per-command
// copies disagreed: "code" fell back to "fast" in ai-ask and to "smart" in
// ai-sum, so the same unbound role resolved to two different models depending
// on which command you asked. The chains below are the union, and every
// command now reads them.
//
// "asr-fast" deliberately does not fall back to "asr": asking for the fast
// transcriber and silently getting the large one is the opposite of the
// request.
//
// "deep" does fall back to "smart", and the asymmetry is deliberate. Asking
// for the slower, better model and getting the ordinary one costs you quality
// you hoped for; asking for the fast one and getting the slow one costs you an
// hour you did not budget. Only the second is a trap.
var fallbacks = map[string][]string{
	RoleFast:    {RoleFast},
	RoleSmart:   {RoleSmart, RoleFast},
	RoleDeep:    {RoleDeep, RoleSmart, RoleFast},
	RoleCode:    {RoleCode, RoleSmart, RoleFast},
	RoleEmbed:   {RoleEmbed},
	RoleASR:     {RoleASR},
	RoleASRFast: {RoleASRFast},
}

// Resolve turns a -m value into a model id. A known role walks its fallback
// chain; anything else is an explicit id and passes through untouched, so both
// spellings work:
//
//	ai ask -m smart -p 'review this design'
//	ai ask -m unsloth/Qwen3-1.7B-UD-Q8_K_XL -p hello
//
// An empty v takes the role in deflt, which is how each subcommand states its
// own preference: ask and tr want "fast", sum wants "smart", hear wants "asr".
func Resolve(v, deflt string) string {
	name := strings.TrimSpace(v)
	if name == "" {
		name = deflt
	}

	chain, ok := fallbacks[name]
	if !ok {
		return name
	}

	// What the config binds beats any built-in default, even a default
	// belonging to an earlier link in the chain.
	for _, step := range chain {
		if r, ok := LookupRole(step); ok {
			if m := Value(r.Key); m != "" {
				return m
			}
		}
	}
	for _, step := range chain {
		if r, ok := LookupRole(step); ok && r.Default != "" {
			return r.Default
		}
	}
	return ""
}

func LookupRole(name string) (Role, bool) {
	i := slices.IndexFunc(Roles, func(r Role) bool { return r.Name == name })
	if i < 0 {
		return Role{}, false
	}
	return Roles[i], true
}

// RoleNames lists the roles for an error message.
func RoleNames() string {
	names := make([]string, len(Roles))
	for i, r := range Roles {
		names[i] = r.Name
	}
	return strings.Join(names, ", ")
}

// Model resolves a role to the model behind it, or "" when the role is unbound
// and has no default. An unbound role is not an error: nobody has to install a
// reranker to transcribe a lecture.
func (r Role) Model() string {
	if v := Value(r.Key); v != "" {
		return v
	}
	return r.Default
}

// BoundRoles returns the roles that actually resolve to a model.
func BoundRoles() []Role {
	var out []Role
	for _, r := range Roles {
		if r.Model() != "" {
			out = append(out, r)
		}
	}
	return out
}

// KeptModels lists models gc must leave alone even though no role names them.
// Roles are not the only way a model gets used: the Kronk server hands models
// to anything that asks it, and those callers name a model directly rather
// than going through a role. Nothing in the config would otherwise mention
// such a model, so gc would read it as garbage.
//
// The value is a comma- or space-separated list of model ids.
func KeptModels() []string {
	return strings.FieldsFunc(Value("AI_MODELS_KEEP"), func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	})
}
