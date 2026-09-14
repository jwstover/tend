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

  ## Functions, pipelines and variables

  A command whose head is a bare word is a call into `Tend.Template.Funcs`,
  which holds the six built-ins the prompts use. Two of them, `and` and `or`,
  short-circuit: their arguments are evaluated one at a time, left to right,
  and the first decisive one is *returned* -- Go's `and` gives back the first
  false argument, or the last argument if none was false, rather than a
  boolean. `{{and .IsBlocked .Bogus}}` on a false `.IsBlocked` is therefore
  not an error, here as in Go.

  A `|` pipeline feeds each stage's value to the next as its *last* argument,
  so `{{.Outcomes | len}}` calls `len` with one argument. Handed to something
  that is not a function, a piped value is the same error as writing the
  argument out: `{{.Cwd | .Input}}` says `Input has arguments but cannot be
  invoked as function`.

  Variables live on a stack, exactly as in Go. `$` is pushed at the bottom
  with the whole data value; `{{$x := ...}}` pushes; `{{$x = ...}}` overwrites
  the nearest binding of that name; `{{if}}` and `{{range}}` pop back to
  where they started, which is what makes an inner `{{$x := ...}}` shadow an
  outer one for the length of the block and no longer. A
  `{{range $i, $o := ...}}` binds the *index* first and the *element*
  second, and one variable (`{{range $o := ...}}`) binds the element, not the
  index.

  `=` with no binding to overwrite is `undefined variable: $x`, at render
  time. The parser refuses an undeclared *read* but cannot refuse an
  undeclared assignment *target* -- to the grammar, the left-hand side of
  `{{$y = 1}}` looks exactly like the left-hand side of `{{$y := 1}}` -- so
  `{{$y = 1}}` and `{{range $i, $o = .L}}` both parse, and Go's `setVar`
  refuses them here.

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
  """

  alias Tend.Template.AST
  alias Tend.Template.Funcs
  alias Tend.Template.RenderError
  alias Tend.Template.Value

  @typep state :: %{source: binary(), dot: term(), vars: [{binary(), term()}]}

  # Go's missingVal: the sentinel a pipeline's first stage is handed, meaning
  # "nothing came before me". `nil` cannot stand in for it -- a piped nil is
  # a real value that a function still has to be given.
  @missing :__missing__

  @doc """
  Renders `nodes`, parsed from `source`, against `data`.

  Raises `Tend.Template.RenderError`.
  """
  @spec render(binary(), [AST.tree_node()], term()) :: binary()
  def render(source, nodes, data) when is_binary(source) and is_list(nodes) do
    # `$` is the bottom of the variable stack, pushed before the first node
    # the way Go's `state` is built, which is why it needs no declaration.
    state = %{source: source, dot: data, vars: [{"$", data}]}
    {iodata, _state} = render_nodes(nodes, state)
    IO.iodata_to_binary(iodata)
  end

  ## Tree nodes

  @spec render_nodes([AST.tree_node()], state()) :: {iodata(), state()}
  defp render_nodes(nodes, state), do: Enum.map_reduce(nodes, state, &render_node/2)

  defp render_node(%AST.Text{text: text}, state), do: {text, state}

  defp render_node(%AST.Action{pipeline: pipeline}, state) do
    {value, state} = eval_pipeline(pipeline, state)

    # An action that declares a variable writes nothing: `{{$x := .Cwd}}` is
    # a binding, not an output. Go decides the same way, on the same list.
    if pipeline.decls == [] do
      {Value.format(value), state}
    else
      {[], state}
    end
  end

  defp render_node(%AST.If{} = node, state) do
    # Only the arm that is taken is evaluated, so an unknown field in the
    # other arm is not an error -- which is why the Go tree validates a
    # prompt against two data sets rather than one.
    #
    # The mark is taken before the condition, as Go's `defer s.pop(s.mark())`
    # is, so a variable declared in either the condition or the body is gone
    # after the `{{end}}`.
    mark = mark(state)
    {value, state} = eval_pipeline(node.pipeline, state)

    {iodata, state} =
      if Value.truthy?(value) do
        render_nodes(node.body, state)
      else
        render_nodes(node.else_body || [], state)
      end

    {iodata, pop(state, mark)}
  end

  defp render_node(%AST.Range{pipeline: pipeline} = node, state) do
    outer = mark(state)
    {value, state} = eval_pipeline(pipeline, state)

    # Go marks the stack *after* the range's own declarations are pushed, so
    # each iteration pops only what the body declared and the loop variables
    # survive to be re-set by the next one.
    body_mark = mark(state)

    {iodata, state} =
      case iterations(value, node, state) do
        [] -> render_nodes(node.else_body || [], state)
        items -> iterate(items, pipeline, node, body_mark, state)
      end

    {iodata, pop(state, outer)}
  end

  defp iterate(items, pipeline, node, body_mark, state) do
    Enum.map_reduce(items, state, fn {index, element}, acc ->
      iteration = bind_loop_vars(%{acc | dot: element}, pipeline, index, element)
      {iodata, acc} = render_nodes(node.body, iteration)

      # Back to the loop's own variables, keeping any assignment the body made
      # to a variable declared outside it -- `pop` truncates, it does not undo.
      {iodata, %{pop(acc, body_mark) | dot: state.dot}}
    end)
  end

  # Go pairs every iteration with an index and an element. For a list the
  # index is the position; for an integer there is no index at all, which is
  # why `{{range $i, $x := 3}}` is an error rather than binding `$i` to nil.
  defp iterations(items, _node, _state) when is_list(items),
    do: items |> Enum.with_index() |> Enum.map(fn {element, index} -> {index, element} end)

  # Go's `walkRange` has a `case reflect.Invalid: break` whose own comment
  # reads "an invalid value is likely a nil map, etc. and acts like an empty
  # map", so a nil interface falls to the {{else}} arm rather than erroring --
  # and so does a nil slice and a nil map, which reach the Slice and Map cases
  # with length zero. Go 1.26.4, "{{range .Outcomes}}x{{else}}none{{end}}":
  # "none" for a nil `any` field, a nil `[]string` field and a nil map alike.
  #
  # It has to sit above the unguarded catch-all below, which is where the
  # `:not_iterable` raise lives; that is the only constraint on where it goes.
  # The integer clause it happens to precede is guarded `when is_integer/1`,
  # so a nil never enters that one whatever the order.
  defp iterations(nil, _node, _state), do: []

  # Go 1.22 gave `range` an integer case: `{{range 3}}` iterates 0, 1, 2 with
  # the cursor bound to the index. `PromptData.Iteration` is an `int64`, so
  # `{{range .Iteration}}` is a prompt a user can already have stored.
  defp iterations(count, node, state) when is_integer(count) do
    # An integer has no index to hand out, so two variables is an error --
    # and it is an error Go raises before it looks at the count, so
    # `{{range $i, $v := 0}}` is refused rather than falling to the
    # {{else}} arm.
    if length(node.pipeline.decls) > 1 do
      raise RenderError.new(
              state.source,
              head_operand(node.pipeline),
              :not_iterable,
              "can't use #{Value.format(count)} to iterate over more than one variable"
            )
    end

    # Go's `walkRange` breaks before the first iteration when the integer is
    # `<= 0`, which lands on the {{else}} arm exactly as an empty list does.
    if count > 0, do: Enum.map(0..(count - 1), &{@missing, &1}), else: []
  end

  defp iterations(value, node, state) do
    raise RenderError.new(
            state.source,
            head_operand(node.pipeline),
            :not_iterable,
            "range can't iterate over #{Value.format(value)}"
          )
  end

  defp bind_loop_vars(state, %AST.Pipeline{decls: []}, _index, _element), do: state

  # `{{range $i, $o = .L}}` assigns to variables that already exist, and Go's
  # assignment outlives the loop because popping the stack only truncates it.
  # Both names were already assigned once, by `declare/3` on the pipeline's
  # own value, so a name with no binding has raised before the first
  # iteration -- exactly where Go raises it.
  defp bind_loop_vars(state, %AST.Pipeline{decls: [one], assign?: true} = pipeline, _i, element),
    do: set_var(state, one.name, element, at(pipeline))

  defp bind_loop_vars(state, %AST.Pipeline{decls: [first, second], assign?: true} = pipe, i, elem) do
    state |> set_var(first.name, i, at(pipe)) |> set_var(second.name, elem, at(pipe))
  end

  # `:=` pushed the declarations while the pipeline was evaluated, so they are
  # the top of the stack: the element is the top one (lexically the second
  # when there are two) and the index the one under it.
  defp bind_loop_vars(
         %{vars: [{name, _} | rest]} = state,
         %AST.Pipeline{decls: [_one]},
         _i,
         elem
       ),
       do: %{state | vars: [{name, elem} | rest]}

  defp bind_loop_vars(state, %AST.Pipeline{decls: [_index, _element]}, index, element) do
    [{element_name, _}, {index_name, _} | rest] = state.vars
    %{state | vars: [{element_name, element}, {index_name, index} | rest]}
  end

  # Go reports a range failure against the ranged expression, not the whole
  # {{range}}, so `{{range .Cwd}}` says `at <.Cwd>`.
  defp head_operand(%AST.Pipeline{commands: [%AST.Command{args: [arg | _]} | _]}), do: arg
  defp head_operand(%AST.Pipeline{} = pipeline), do: pipeline

  ## Pipelines and commands

  # Each stage's value is the next stage's *last* argument, and the
  # declarations, if any, bind the value the last stage produced.
  defp eval_pipeline(%AST.Pipeline{} = pipeline, state) do
    {value, state} =
      Enum.reduce(pipeline.commands, {@missing, state}, fn command, {final, state} ->
        eval_command(command, final, state)
      end)

    {value, declare(pipeline, value, state)}
  end

  defp declare(%AST.Pipeline{decls: decls, assign?: assign?} = pipeline, value, state) do
    Enum.reduce(decls, state, fn decl, state ->
      if assign? do
        set_var(state, decl.name, value, at(pipeline))
      else
        push(state, decl.name, value)
      end
    end)
  end

  defp eval_command(%AST.Command{args: [head | args]} = command, final, state) do
    case head do
      %AST.Identifier{name: name} ->
        eval_function(name, head, command, args, final, state)

      %AST.Field{path: path} ->
        {eval_field_chain(state.dot, path, head, args, final, state), state}

      %AST.Variable{} ->
        eval_variable(head, command, args, final, state)

      %AST.Pipeline{} ->
        # A parenthesised pipeline holds all its own arguments, so anything
        # beside it -- or piped into it -- is an error.
        not_a_function(command, head, args, final, state)
        eval_pipeline(head, state)

      %AST.Nil{} ->
        not_a_function(command, head, args, final, state)
        raise RenderError.new(state.source, command, :bad_command, "nil is not a command")

      _literal ->
        not_a_function(command, head, args, final, state)
        {literal(head, state), state}
    end
  end

  defp literal(%AST.Dot{}, state), do: state.dot
  defp literal(%AST.String{value: value}, _state), do: value
  defp literal(%AST.Number{value: value}, _state), do: value
  defp literal(%AST.Bool{value: value}, _state), do: value

  ## Function calls

  defp eval_function(name, node, command, args, final, state) do
    # Go resolves function names at parse time and refuses an unknown one
    # there; this parser has no function table, so the name is resolved here
    # and gets Go's parse-time wording.
    unless Funcs.defined?(name) do
      raise RenderError.new(
              state.source,
              command,
              :undefined_function,
              ~s(function "#{name}" not defined)
            )
    end

    case Funcs.check_arity(name, length(args), final != @missing) do
      :ok -> :ok
      {:error, detail} -> raise RenderError.new(state.source, node, :wrong_args, detail)
    end

    if Funcs.short_circuit?(name) do
      short_circuit(args, name == "or", final, @missing, state)
    else
      call(name, args, final, command, state)
    end
  end

  # Go's special case for `and` and `or`: evaluate arguments until one is
  # decisive and return *that argument's value*. Nothing after it is
  # evaluated, so a field that would fail is never reached.
  defp short_circuit([], _decisive, @missing, value, state), do: {value, state}
  defp short_circuit([], _decisive, final, _value, state), do: {final, state}

  defp short_circuit([arg | rest], decisive, final, _value, state) do
    {value, state} = eval_operand(arg, state)

    if Value.truthy?(value) == decisive do
      {value, state}
    else
      short_circuit(rest, decisive, final, value, state)
    end
  end

  defp call(name, args, final, command, state) do
    {values, state} = Enum.map_reduce(args, state, &eval_operand/2)
    values = if final == @missing, do: values, else: values ++ [final]

    case Funcs.call(name, values) do
      {:ok, value} ->
        {value, state}

      {:error, detail} ->
        raise RenderError.new(
                state.source,
                command,
                :call_error,
                "error calling #{name}: #{detail}"
              )
    end
  end

  ## Variables

  defp eval_variable(%AST.Variable{name: name, path: []} = head, command, args, final, state) do
    not_a_function(command, head, args, final, state)
    {var_value(state, name, head), state}
  end

  defp eval_variable(%AST.Variable{name: name, path: path} = head, _command, args, final, state) do
    base = var_value(state, name, head)
    {eval_field_chain(base, path, head, args, final, state), state}
  end

  defp var_value(state, name, node) do
    case List.keyfind(state.vars, name, 0) do
      {^name, value} -> value
      nil -> raise undefined_variable(state, name, node)
    end
  end

  defp push(state, name, value), do: %{state | vars: [{name, value} | state.vars]}

  # Go's setVar walks the stack for the nearest binding of the name and calls
  # `s.errorf` when there is none, so `{{$y = 1}}` on a variable that was
  # never declared is a *render* error. The parser cannot catch it: it tracks
  # declarations and refuses an undeclared *read*, but an assignment's
  # left-hand side is a declaration site as far as the grammar is concerned,
  # so `{{$y = 1}}` and `{{range $i, $o = .L}}` both parse. Declaring the
  # name here instead would render a template Go refuses.
  defp set_var(state, name, value, node) do
    unless List.keymember?(state.vars, name, 0) do
      raise undefined_variable(state, name, node)
    end

    %{state | vars: replace_var(state.vars, name, value)}
  end

  defp undefined_variable(state, name, node) do
    RenderError.new(state.source, node, :undefined_variable, "undefined variable: #{name}")
  end

  # Go's `s.at` is the last node it looked at, and this approximates it with
  # the head of the pipeline's *last* command. The two agree whenever that
  # command has no arguments -- `{{$y = 1}}` reports `at <1>` in both, and
  # `{{range $i, $o = .Outcomes}}` reports `at <.Outcomes>` in both -- which
  # covers every assignment a stored prompt has.
  #
  # They part company once the command takes arguments, because Go's
  # `evalCall` calls `s.at(args[i])` per argument, so its `s.at` ends up on
  # the last *argument* rather than on the head: `{{$y = len .Cwd}}` is
  # `at <.Cwd>` in Go and `at <len>` here, and `{{$y = and .Cwd .Input}}` is
  # `at <.Input>` there and `at <and>` here. Only the reported context and
  # its column differ; the reason and the detail match Go in every case.
  # This is the same class of drift as the `{{.Cwd | (.Input)}}` position
  # the moduledoc already declares.
  defp at(%AST.Pipeline{commands: commands} = pipeline) do
    case List.last(commands) do
      %AST.Command{args: [arg | _]} -> arg
      _none -> pipeline
    end
  end

  defp replace_var([{name, _} | rest], name, value), do: [{name, value} | rest]
  defp replace_var([entry | rest], name, value), do: [entry | replace_var(rest, name, value)]

  defp mark(state), do: length(state.vars)

  # Go's pop truncates the stack to a mark. Entries below the mark keep
  # whatever an inner `{{$x = ...}}` assigned them.
  defp pop(state, mark), do: %{state | vars: Enum.drop(state.vars, length(state.vars) - mark)}

  ## Arguments given to something that is not a function

  defp not_a_function(_command, _head, [], @missing, _state), do: :ok

  defp not_a_function(command, %AST.Pipeline{} = head, _args, _final, state) do
    # Go's notAFunction formats `args[0]`, which for a parenthesised
    # sub-expression is the whole `PipeNode`: `{{(and 1 2) 3}}` is
    # `can't give argument to non-function and 1 2`, not `... non-function
    # and`. The parentheses are the *command*'s, not the pipeline's, so
    # `PipeNode.String()` leaves them off and so does `AST.to_source/1`.
    #
    # Only the position differs from the clause below: Go reports the whole
    # command here, `at <(.Cwd) .Input>`. One known drift, left alone
    # deliberately: for `{{.Cwd | (.Input)}}` Go reports `at <.Cwd>`, a stale
    # `s.at` from the previous stage, where this reports `at <(.Input)>` --
    # the stage that actually failed.
    raise RenderError.new(
            state.source,
            command,
            :bad_command,
            "can't give argument to non-function #{AST.to_source(head)}"
          )
  end

  defp not_a_function(_command, head, _args, _final, state) do
    raise RenderError.new(
            state.source,
            head,
            :bad_command,
            "can't give argument to non-function #{AST.to_source(head)}"
          )
  end

  ## Operands

  defp eval_operand(%AST.Dot{}, state), do: {state.dot, state}

  defp eval_operand(%AST.Field{path: path} = node, state),
    do: {eval_field_chain(state.dot, path, node, [], @missing, state), state}

  defp eval_operand(%AST.Variable{} = node, state),
    do: eval_variable(node, node, [], @missing, state)

  # A bare word in argument position is a call with no arguments of its own.
  defp eval_operand(%AST.Identifier{name: name} = node, state),
    do: eval_function(name, node, node, [], @missing, state)

  defp eval_operand(%AST.String{value: value}, state), do: {value, state}
  defp eval_operand(%AST.Number{value: value}, state), do: {value, state}
  defp eval_operand(%AST.Bool{value: value}, state), do: {value, state}
  defp eval_operand(%AST.Nil{}, state), do: {nil, state}
  defp eval_operand(%AST.Pipeline{} = pipeline, state), do: eval_pipeline(pipeline, state)

  ## Field access -- the missingkey=error half

  # `.A.B.C arg` walks A and B with no arguments and hands them to C, which is
  # the only name that could have been a method.
  defp eval_field_chain(receiver, path, node, args, final, state) do
    {leading, [last]} = Enum.split(path, -1)
    receiver = walk(receiver, leading, node, state)
    index = length(leading)

    cond do
      args == [] and final == @missing -> field(receiver, last, index, node, state)
      # Go checks the receiver for nil before it objects to the arguments.
      is_nil(receiver) -> field(receiver, last, index, node, state)
      true -> raise field_argument_error(receiver, last, node, state)
    end
  end

  # Go has three messages here, not one. A map key is "not a method"; a
  # struct field that exists "cannot be invoked as function"; and a name the
  # receiver does not have is the plain missing-field error, because Go looks
  # the field up before it notices the arguments.
  defp field_argument_error(receiver, name, node, state) do
    cond do
      is_map(receiver) and not is_struct(receiver) ->
        RenderError.new(
          state.source,
          node,
          :bad_command,
          "#{name} is not a method but has arguments"
        )

      is_struct(receiver) and match?({:ok, _value}, fetch(receiver, name)) ->
        RenderError.new(
          state.source,
          node,
          :bad_command,
          "#{name} has arguments but cannot be invoked as function"
        )

      true ->
        missing_field(state, node, name, Value.type_name(receiver))
    end
  end

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
    raise missing_field(state, node, name, Value.type_name(other))
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
end
