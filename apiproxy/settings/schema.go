package settingsapi

// Schemas describe Goren's exposed input, not a vendor or Harness wire schema.
// Owner validation still decides semantic constraints after source resolution.
const providerSchema = `{
  "$schema":"https://json-schema.org/draft/2020-12/schema",
  "type":"object","additionalProperties":false,
  "properties":{
    "apiKeyEnv":{"type":"string","pattern":"^[A-Za-z_][A-Za-z0-9_]*$"},
    "baseURL":{"type":"string","format":"uri"},
    "streamIdleTimeoutMs":{"type":"number","exclusiveMinimum":0,"maximum":2147483647},
    "retryPolicy":{"type":"object","additionalProperties":false,"properties":{
      "mode":{"type":"string","enum":["normal","always"]},
      "maxRetries":{"type":"integer","minimum":0},
      "retryableCodes":{"type":"array","items":{"type":"string"}},
      "backoff":{"type":"object","additionalProperties":false,"properties":{
        "initialDelayMs":{"type":"number","minimum":0},
        "maxDelayMs":{"type":"number","minimum":0},
        "jitterRatio":{"type":"number","minimum":0,"maximum":1}
      }}
    }}
  }
}`

const promptSchema = `{
  "$schema":"https://json-schema.org/draft/2020-12/schema",
  "type":"object","additionalProperties":false,
  "properties":{
    "includeHarnessIdentity":{"type":"boolean"},
    "includeRuntimeContext":{"type":"boolean"},
    "persona":{"type":"string"},
    "toolOrder":{"type":"array","minItems":1,"items":{"type":"string"},"contains":{"const":"<unlisted-tools>"}}
  }
}`

const mcpSchema = `{
  "$schema":"https://json-schema.org/draft/2020-12/schema",
  "type":"object","additionalProperties":false,
  "required":["enabled","connection"],
  "properties":{
    "enabled":{"type":"boolean"},
    "connection":{
      "type":"object","additionalProperties":false,
      "required":["serverName","transport","url"],
      "properties":{
        "serverName":{"type":"string","minLength":1},
        "transport":{"const":"streamable-http"},
        "url":{"type":"string","format":"uri"},
        "skipDiscover":{"type":"boolean"},
        "requestTimeoutMs":{"type":"integer","minimum":0},
        "elicitationTimeoutMs":{"type":"integer","minimum":0},
        "maxInputRoundTrips":{"type":"integer","minimum":0},
        "maxTools":{"type":"integer","minimum":0},
        "maxPages":{"type":"integer","minimum":0},
        "toolResultLimit":{"type":"integer","minimum":0}
      }
    }
  }
}`

const toolSchema = `{
 "$schema":"https://json-schema.org/draft/2020-12/schema",
 "type":"object","additionalProperties":false,
 "properties":{
  "enabled":{"type":"boolean","default":true},
  "visible":{"type":"boolean","default":true}
 }
}`

const companionSchema = `{
 "$schema":"https://json-schema.org/draft/2020-12/schema",
 "type":"object","additionalProperties":false,
 "properties":{
  "enabled":{"type":"boolean"},
  "inherit":{"type":"object","additionalProperties":false,"properties":{
   "model":{"type":"boolean"},"prompt":{"type":"boolean"},
   "tools":{"enum":["none","all"]},"mcp":{"type":"boolean"}
  }},
  "model":{"type":"object","additionalProperties":false,"properties":{
   "provider":{"type":"string"},"model":{"type":"string"},
   "maxTokens":{"type":"integer","minimum":1,"maximum":9007199254740991}
  }},
  "prompt":` + promptSchema + `,
  "tools":{"type":"object","additionalProperties":` + toolSchema + `},
  "mcp":{"type":"array","items":{"$ref":"#/$defs/mcp/properties/connection"}}
 },
 "$defs":{"mcp":` + mcpSchema + `}
}`
