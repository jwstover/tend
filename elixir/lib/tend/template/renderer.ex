defmodule Tend.Template.Renderer do
  @moduledoc """
  Evaluates a `Tend.Template.AST` node list against data.

  A transliteration of `text/template`'s `exec.go` for the node types the
  parser can produce, with the `missingkey=error` option always on -- the Go
  tree sets it in `parsePrompt` and never offers the alternative, so neither
  does this.

  ## Resolving a field against Elixir data

  A prompt says `{{.Task.Title}}`, in Go's exported-field spelling, and the
  data it renders against is an Elixir struct or map with idiomatic keys. A
  name is looked up in this order, first hit winning:

    1. the name verbatim as a string key -- `"Title"`, for a plain map
    2. the name verbatim as an existing atom -- `:Title`
    3. the name under `Macro.underscore/1` as an existing atom -- `:title`,
       and likewise `IsBlocked` -> `:is_blocked`, `ID` -> `:id`,
       `DependsOn` -> `:depends_on`

  Structs only ever have atom keys, so in practice a struct resolves by rule
  3. Nothing here creates an atom: an unknown name is a missing field, which
  is the whole point of `missingkey=error`, and `String.to_existing_atom/1`
  keeps a hostile prompt from filling the atom table.

  Go's `evalField` tries a *method* of the name first. There is no analogue
  -- a `PromptData` is data, not behaviour -- so that step has no counterpart.

  ## What the data has to look like

  The renderer is faithful to Go, which means the value types have to line up
  with the Go struct's, or the output diverges:

    * a string field must hold `""` when it is empty, never `nil`. Go's
      zero string prints as nothing; an Elixir `nil` prints as `<no value>`,
      which is what Go prints for a nil interface.
    * a list field must hold `[]` when it is empty, never `nil`, or
      `{{range}}` over it raises instead of taking the `{{else}}` arm --
      again exactly as Go treats an untyped nil.
    * a `{{range}}` target must be a list or an integer. Go 1.22 added the
      integer case -- `{{range 3}}` iterates 0, 1, 2 -- and `PromptData`'s
      `Iteration` is an `int64`, so `{{range .Iteration}}` is a prompt a user
      can already have stored; it renders here exactly as Go renders it. Go
      also ranges over a *map*, which no field of `PromptData` is, so that
      one still raises `:not_iterable` rather than inventing an iteration
      order (Go's is sorted by key). Go does **not** range over a string, and
      neither does this: `{{range .Cwd}}` is an error in both.

  The prompt-data structs are the workflow sub-task's to define; this is the
  contract they have to meet.

  ## Deferred to the builtins sub-task

  Function calls (`and`, `or`, `not`, `eq`, `ne`, `len`), multi-stage
  pipelines (`{{.Outcomes | len}}`) and variable declarations
  (`{{range $i, $o := .Outcomes}}`) are not evaluated here. They raise a
  `Tend.Template.RenderError` with reason `:unsupported`, worded the way Go
  words an undefined function, and the builtins sub-task replaces the raise
  with a function table. The root variable `$` *is* resolved, because it is
  the one variable that needs no declaration.
  """

  alias Tend.Template.AST
  alias Tend.Template.RenderError

  @typep state :: %{source: binary(), dot: term(), root: term()}

  @doc """
  Renders `nodes`, parsed from `source`, against `data`.

  Raises `Tend.Template.RenderError`.
  """
  @spec render(binary(), [AST.tree_node()], term()) :: binary()
  def render(source, nodes, data) when is_binary(source) and is_list(nodes) do
    nodes
    |> render_nodes(%{source: source, dot: data, root: data})
    |> IO.iodata_to_binary()
  end

  ## Tree nodes

  @spec render_nodes([AST.tree_node()], state()) :: iodata()
  defp render_nodes(nodes, state), do: Enum.map(nodes, &render_node(&1, state))

  defp render_node(%AST.Text{text: text}, _state), do: text

  defp render_node(%AST.Action{pipeline: pipeline}, state) do
    pipeline |> eval_pipeline(state) |> format()
  end

  defp render_node(%AST.If{} = node, state) do
    # Only the arm that is taken is evaluated, so an unknown field in the
    # other arm is not an error -- which is why the Go tree validates a
    # prompt against two data sets rather than one.
    if truthy?(eval_pipeline(node.pipeline, state)) do
      render_nodes(node.body, state)
    else
      render_nodes(node.else_body || [], state)
    end
  end

  defp render_node(%AST.Range{} = node, state) do
    case node.pipeline |> eval_pipeline(state) |> iterable(node, state) do
      [] -> render_nodes(node.else_body || [], state)
      items -> Enum.map(items, &render_nodes(node.body, %{state | dot: &1}))
    end
  end

  defp iterable(items, _node, _state) when is_list(items), do: items

  # Go 1.22 gave `range` an integer case: `{{range 3}}` iterates 0, 1, 2 with
  # the cursor bound to the index. `PromptData.Iteration` is an `int64`, so
  # `{{range .Iteration}}` is a prompt a user can already have stored.
  defp iterable(count, _node, _state) when is_integer(count) and count > 0,
    do: Enum.to_list(0..(count - 1))

  # Go's `walkRange` breaks before the first iteration when the integer is
  # `<= 0`, which lands on the {{else}} arm exactly as an empty list does.
  defp iterable(count, _node, _state) when is_integer(count), do: []

  defp iterable(value, node, state) do
    raise RenderError.new(
            state.source,
            head_operand(node.pipeline),
            :not_iterable,
            "range can't iterate over #{format(value)}"
          )
  end

  # Go reports a range failure against the ranged expression, not the whole
  # {{range}}, so `{{range .Cwd}}` says `at <.Cwd>`.
  defp head_operand(%AST.Pipeline{commands: [%AST.Command{args: [arg | _]} | _]}), do: arg
  defp head_operand(%AST.Pipeline{} = pipeline), do: pipeline

  ## Pipelines and commands

  defp eval_pipeline(%AST.Pipeline{decls: [decl | _]} = pipeline, state) do
    raise RenderError.new(
            state.source,
            pipeline,
            :unsupported,
            ~s(variable declaration #{decl.name} is not supported yet)
          )
  end

  defp eval_pipeline(%AST.Pipeline{commands: [command]}, state), do: eval_command(command, state)

  defp eval_pipeline(%AST.Pipeline{} = pipeline, state) do
    raise RenderError.new(
            state.source,
            pipeline,
            :unsupported,
            "multi-stage pipelines are not supported yet"
          )
  end

  # A bare word heads a command only as a function name, and there is no
  # function table until the builtins sub-task, so every one of them gets
  # Go's message for a name it does not know.
  defp eval_command(%AST.Command{args: [%AST.Identifier{name: name} | _]} = command, state) do
    raise RenderError.new(
            state.source,
            command,
            :unsupported,
            ~s(function "#{name}" not defined)
          )
  end

  defp eval_command(%AST.Command{args: [%AST.Nil{}]} = command, state) do
    raise RenderError.new(state.source, command, :bad_command, "nil is not a command")
  end

  defp eval_command(%AST.Command{args: [arg]}, state), do: eval_operand(arg, state)

  defp eval_command(%AST.Command{args: [head | _]} = command, state) do
    # Go resolves the head before it objects to the arguments, so
    # `{{.Nope .Cwd}}` is still `can't evaluate field Nope`, not an argument
    # error. Evaluating it here and throwing the value away keeps that order.
    _value = eval_operand(head, state)

    raise argument_error(command, head, state)
  end

  # Go has three messages here, not one. A field chain off a map is "not a
  # method"; off anything else it "cannot be invoked as function"; and only a
  # head that is no field at all -- `$`, the cursor, a literal, a
  # parenthesised pipeline -- gets "can't give argument to non-function".
  defp argument_error(_command, %AST.Field{path: path} = head, state),
    do: field_argument_error(head, state.dot, path, state)

  defp argument_error(_command, %AST.Variable{name: "$", path: [_ | _] = path} = head, state),
    do: field_argument_error(head, state.root, path, state)

  defp argument_error(command, %AST.Pipeline{} = head, state) do
    # Go names the pipeline's own head in the message and the whole command
    # in the context: `at <(.Cwd) .Input>: ... non-function .Cwd`.
    RenderError.new(
      state.source,
      command,
      :bad_command,
      "can't give argument to non-function #{AST.to_source(head_operand(head))}"
    )
  end

  defp argument_error(_command, head, state) do
    RenderError.new(
      state.source,
      head,
      :bad_command,
      "can't give argument to non-function #{AST.to_source(head)}"
    )
  end

  defp field_argument_error(head, base, path, state) do
    receiver = walk(base, Enum.drop(path, -1), head, state)
    name = List.last(path)

    detail =
      if is_map(receiver) and not is_struct(receiver) do
        "#{name} is not a method but has arguments"
      else
        "#{name} has arguments but cannot be invoked as function"
      end

    RenderError.new(state.source, head, :bad_command, detail)
  end

  ## Operands

  defp eval_operand(%AST.Dot{}, state), do: state.dot
  defp eval_operand(%AST.Field{path: path} = node, state), do: walk(state.dot, path, node, state)

  defp eval_operand(%AST.Variable{name: "$", path: path} = node, state),
    do: walk(state.root, path, node, state)

  defp eval_operand(%AST.Variable{name: name} = node, state) do
    raise RenderError.new(
            state.source,
            node,
            :unsupported,
            ~s(variable #{name} is not supported yet)
          )
  end

  defp eval_operand(%AST.String{value: value}, _state), do: value
  defp eval_operand(%AST.Number{value: value}, _state), do: value
  defp eval_operand(%AST.Bool{value: value}, _state), do: value
  defp eval_operand(%AST.Nil{}, _state), do: nil
  defp eval_operand(%AST.Pipeline{} = pipeline, state), do: eval_pipeline(pipeline, state)

  ## Field access -- the missingkey=error half

  defp walk(value, path, node, state) do
    path
    |> Enum.with_index()
    |> Enum.reduce(value, fn {name, index}, acc -> field(acc, name, index, node, state) end)
  end

  # Go says `nil data; no entry for key "X"` only for a nil *root*.
  defp field(nil, name, 0, node, state) do
    raise RenderError.new(
            state.source,
            node,
            :nil_data,
            ~s(nil data; no entry for key "#{name}")
          )
  end

  # Partway down a chain Go says `nil pointer evaluating T.X`, naming the Go
  # type of the pointer it could not follow. An Elixir nil carries no type,
  # so the type slot is `nil` -- the same substitution every other message
  # here makes when Go names a type. `Tend.Template.RenderError` says so.
  defp field(nil, name, _index, node, state) do
    raise RenderError.new(
            state.source,
            node,
            :nil_data,
            "nil pointer evaluating nil.#{name}"
          )
  end

  defp field(%module{} = struct, name, _index, node, state) do
    case fetch(struct, name) do
      {:ok, value} -> value
      :error -> raise missing_field(state, node, name, inspect(module))
    end
  end

  defp field(map, name, _index, node, state) when is_map(map) do
    case fetch(map, name) do
      {:ok, value} ->
        value

      :error ->
        raise RenderError.new(
                state.source,
                node,
                :missing_key,
                ~s(map has no entry for key "#{name}")
              )
    end
  end

  defp field(other, name, _index, node, state) do
    raise missing_field(state, node, name, type_name(other))
  end

  defp missing_field(state, node, name, type) do
    RenderError.new(
      state.source,
      node,
      :missing_field,
      "can't evaluate field #{name} in type #{type}"
    )
  end

  defp fetch(map, name) do
    name
    |> keys()
    |> Enum.find_value(:error, fn key ->
      case Map.fetch(map, key) do
        {:ok, value} -> {:ok, value}
        :error -> nil
      end
    end)
  end

  # `__struct__` is Elixir's, not the data's: Go has no concept of it, and
  # `{{.__struct__}}` has to be the miss `{{.Bogus}}` is rather than printing
  # a module name. The string key is left alone -- a plain map with a literal
  # `"__struct__"` key is a map Go would have resolved too.
  @reserved_atoms [:__struct__, :__exception__]

  defp keys(name) do
    atoms =
      [existing_atom(name), name |> Macro.underscore() |> existing_atom()]
      |> Enum.reject(&(is_nil(&1) or &1 in @reserved_atoms))

    Enum.uniq([name | atoms])
  end

  # Never String.to_atom/1: a prompt is user input, and the atom table is not
  # garbage collected.
  defp existing_atom(name) do
    String.to_existing_atom(name)
  rescue
    ArgumentError -> nil
  end

  defp type_name(value) when is_binary(value), do: "binary"
  defp type_name(value) when is_integer(value), do: "integer"
  defp type_name(value) when is_float(value), do: "float"
  defp type_name(value) when is_boolean(value), do: "boolean"
  defp type_name(value) when is_list(value), do: "list"
  defp type_name(value) when is_atom(value), do: "atom"
  defp type_name(value) when is_tuple(value), do: "tuple"
  defp type_name(value) when is_function(value), do: "function"
  defp type_name(_value), do: "term"

  ## Go's truthiness

  # An empty string, an empty list and an empty map are false, as are zero
  # and nil; a struct is always true, however empty it is. The one that bites
  # is the number: `{{if $i}}` on index 0 takes the {{else}} arm.
  defp truthy?(nil), do: false
  defp truthy?(false), do: false
  defp truthy?(true), do: true
  defp truthy?(""), do: false
  defp truthy?([]), do: false
  defp truthy?(value) when is_number(value), do: value != 0
  defp truthy?(%_{}), do: true
  defp truthy?(value) when is_map(value), do: map_size(value) > 0
  defp truthy?(_value), do: true

  ## Go's %v

  # `fmt.Fprint` on a nil interface, which is what an absent Elixir value is
  # closest to.
  defp format(nil), do: "<no value>"
  defp format(true), do: "true"
  defp format(false), do: "false"
  defp format(value) when is_binary(value), do: value
  defp format(value) when is_integer(value), do: Integer.to_string(value)
  defp format(value) when is_float(value), do: format_float(value)
  defp format(value) when is_list(value), do: "[" <> Enum.map_join(value, " ", &format/1) <> "]"

  # Go prints a struct's fields in declaration order, and so does this.
  # `defstruct` records the order and `Module.__info__(:struct)` hands it back
  # in it, so a bare `{{.Subtasks}}` is Go's bytes exactly as long as the
  # Elixir struct declares its fields in the Go struct's order -- which the
  # prompt-data structs have to anyway, since they are the same struct.
  # `Map.from_struct/1` would give atom table order, which is neither Go's nor
  # the same twice.
  defp format(%module{} = struct) do
    "{" <> Enum.map_join(field_values(struct, module), " ", &format/1) <> "}"
  end

  defp format(value) when is_map(value) do
    body =
      value
      |> Enum.sort()
      |> Enum.map_join(" ", fn {key, entry} -> "#{format(key)}:#{format(entry)}" end)

    "map[" <> body <> "]"
  end

  defp format(value) when is_atom(value), do: Atom.to_string(value)
  defp format(value), do: inspect(value)

  # A struct whose module is not loaded -- a hand-built `%{__struct__: Mod}`,
  # which no prompt data is -- has no declaration order to recover, so its
  # fields are sorted by name: still not Go's order, but the same every run.
  defp field_values(struct, module) do
    case declared_fields(module) do
      fields when is_list(fields) -> Enum.map(fields, &Map.get(struct, &1.field))
      _other -> struct |> Map.from_struct() |> Enum.sort() |> Enum.map(&elem(&1, 1))
    end
  end

  defp declared_fields(module) do
    if Code.ensure_loaded?(module), do: module.__info__(:struct)
  rescue
    UndefinedFunctionError -> nil
  end

  # Go's %v for a float is %g at the shortest precision that round-trips, so
  # 1.0 prints as `1`. Exponent formatting past that is Erlang's, not Go's;
  # no prompt has a float in it, and only a literal could put one there.
  defp format_float(value) do
    truncated = trunc(value)

    if value == truncated do
      Integer.to_string(truncated)
    else
      :erlang.float_to_binary(value, [:short])
    end
  end
end
