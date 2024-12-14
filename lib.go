package climate

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/pb33f/libopenapi"
	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"
	"github.com/pb33f/libopenapi/orderedmap"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

type ParamMeta struct {
	Name string
	Type string // Same as the type name in OpenAPI
}

type HandlerData struct {
	Method           string      // the HTTP method
	Path             string      // the parameterised path
	PathParams       []ParamMeta // List of path params
	QueryParams      []ParamMeta // List of query params
	HeaderParams     []ParamMeta // List of header params
	CookieParams     []ParamMeta // List of cookie params
	RequestBodyParam ParamMeta   // The request body
}

type Handler func(opts *cobra.Command, args []string, data HandlerData)

type extensions struct {
	hidden  bool
	aliases []string
	group   string
	ignored bool
}

func parseExtensions(exts *orderedmap.Map[string, *yaml.Node]) (*extensions, error) {
	ex := extensions{}
	aliases := []string{}

	for ext, val := range exts.FromOldest() {
		var opts any
		if err := val.Decode(&opts); err != nil {
			return nil, err
		}

		switch ext {
		case "x-cli-hidden":
			ex.hidden = opts.(bool)
		case "x-cli-aliases":
			for _, alias := range opts.([]any) {
				aliases = append(aliases, alias.(string))
			}
			ex.aliases = aliases
		case "x-cli-group":
			ex.group = opts.(string)
		case "x-cli-ignored":
			ex.ignored = opts.(bool)
		}
	}

	return &ex, nil
}

// Loads and verifies an OpenAPI spec frpm an array of bytes
func LoadV3(data []byte) (*libopenapi.DocumentModel[v3.Document], error) {
	document, err := libopenapi.NewDocument(data)
	if err != nil {
		return nil, err
	}

	model, errors := document.BuildV3Model()
	for _, err := range errors {
		return nil, fmt.Errorf("Cannot create v3 model: %e", err)
	}

	return model, nil
}

// Loads and verifies an OpenAPI spec from a file path
func LoadFileV3(path string) (*libopenapi.DocumentModel[v3.Document], error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	return LoadV3(data)
}

// Bootstraps a cobra.Command with the loaded model and a handler map
func BootstrapV3(rootCmd *cobra.Command, model libopenapi.DocumentModel[v3.Document], handlers map[string]Handler) error {
	cmdGroups := make(map[string][]cobra.Command)

	for path, item := range model.Model.Paths.PathItems.FromOldest() {
		for method, op := range item.GetOperations().FromOldest() {
			cmd := cobra.Command{}
			exts, err := parseExtensions(op.Extensions)
			if err != nil {
				return err
			}

			if exts.ignored {
				continue
			}

			flags := cmd.Flags()
			queryParams := []ParamMeta{}
			pathParams := []ParamMeta{}
			headerParams := []ParamMeta{}
			cookieParams := []ParamMeta{}
			hData := HandlerData{Method: method, Path: path}

			for _, param := range op.Parameters {
				t := param.Schema.Schema().Type[0]

				switch t {
				case "string":
					flags.String(param.Name, "", param.Description)
				case "integer":
					flags.Int(param.Name, 0, param.Description)
				case "number":
					flags.Float64(param.Name, 0.0, param.Description)
				case "boolean":
					flags.Bool(param.Name, false, param.Description)
				default:
					// TODO: array, object
					slog.Warn("TODO: Unhandled param", "name", param.Name, "type", param.Schema.Schema().Type[0])
					continue
				}

				meta := ParamMeta{Name: param.Name, Type: t}
				switch param.In {
				case "path":
					pathParams = append(pathParams, meta)
				case "query":
					queryParams = append(queryParams, meta)
				case "header":
					headerParams = append(headerParams, meta)
				case "cookie":
					cookieParams = append(cookieParams, meta)
				}

				if req := param.Required; req != nil && *req {
					cmd.MarkFlagRequired(param.Name)
				}
			}

			hData.QueryParams = queryParams
			hData.PathParams = pathParams
			hData.HeaderParams = headerParams
			hData.CookieParams = cookieParams

			if body := op.RequestBody; body != nil {
				// TODO: hammock on ways to handle the req bodies. Maybe take in a stdin?
				bExts, err := parseExtensions(body.Extensions)
				if err != nil {
					return err
				}

				paramName := "climate-data"
				if aliases := bExts.aliases; len(aliases) > 0 {
					paramName = aliases[0]
				}

				for mime, kind := range body.Content.FromOldest() {
					t := kind.Schema.Schema().Type[0]
					hData.RequestBodyParam = ParamMeta{Name: paramName, Type: t}

					switch t {
					case "object":
						flags.String(paramName, "", body.Description)
						if req := body.Required; req != nil && *req {
							cmd.MarkFlagRequired(paramName)
						}
					default:
						slog.Warn("TODO: Unhandled request body type", "mime", mime, "type", kind.Schema.Schema().Type[0])
					}
				}
			}

			handler, ok := handlers[op.OperationId]
			if !ok {
				slog.Warn("Ho handler defined, skipping", "id", op.OperationId)
				continue
			}

			cmd.Hidden = exts.hidden
			cmd.Short = op.Description
			if op.Summary != "" {
				cmd.Short = op.Summary
			}
			cmd.Run = func(opts *cobra.Command, args []string) {
				// TODO: Interpolate path
				handler(opts, args, hData)
			}

			// TODO: hammock on better ways to handle aliases, prefers the first one as of now
			cmd.Use = op.OperationId // default
			if aliases := exts.aliases; len(exts.aliases) > 0 {
				cmd.Use = aliases[0]
				cmd.Aliases = aliases[1:]
			}

			if g := exts.group; g != "" {
				_, ok := cmdGroups[g]
				if !ok {
					cmdGroups[g] = []cobra.Command{}
				}
				cmdGroups[g] = append(cmdGroups[g], cmd)
			} else {
				rootCmd.AddCommand(&cmd)
			}
		}
	}

	for group, cmds := range cmdGroups {
		groupedCmd := cobra.Command{
			Use:   group,
			Short: fmt.Sprintf("Operations on %s", group),
		}

		for _, cmd := range cmds {
			groupedCmd.AddCommand(&cmd)
		}

		rootCmd.AddCommand(&groupedCmd)
	}

	return nil
}
