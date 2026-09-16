defmodule Tend.Template.Parser do
  @moduledoc """
  Builds a `Tend.Template.AST` node list from `Tend.Template.Lexer` tokens.

  A transliteration of the recursive descent in `text/template/parse/parse.go`,
  down to the order the checks run in, because the order is what decides which
  of two plausible messages a broken prompt gets.

  Two deliberate departures from Go, both forced by this being part one of
  three:

    * **No function table.** Go resolves `{{and ...}}` at parse time and says
      `function "and" not defined` for anything it does not know. There is no
      function table until the builtins sub-task, so every bare word parses
      into a `Tend.Template.AST.Identifier` and is resolved later.
    * **No `{{template}}`, `{{define}}`, `{{block}}`, `{{with}}`, `{{break}}`
      or `{{continue}}`.** None appears in any stored prompt or fixture. They
      lex as keywords and are refused by name, so the error says what is
      unsupported rather than something about a stray word.

  There is one construct Go accepts and this refuses: a field chain whose
  head is neither a field nor a variable -- Go's `ChainNode`, as in
  `{{len.A}}` or `{{(.A).B}}`. `Tend.Template` lists it with the rest of what
  is out of the subset, and `extend/3` refuses it by name.

  Variable scope *is* tracked, because Go tracks it at parse time: `{{$o}}`
  outside the `{{range}}` that declared it is a parse error in Go and here
  too, and that matters for the TUI's validate action, which has no data to
  render against.
  """

  alias Tend.Template.AST
  alias Tend.Template.Lexer
  alias Tend.Template.ParseError

  # Token types that can begin a command, from Go's pipeline().
  @operand_starts [
    :bool,
    :char_constant,
    :dot,
    :field,
    :identifier,
    :number,
    :nil_literal,
    :string,
    :variable,
    :left_paren
  ]

  # Operand types that cannot head a pipeline stage after the first.
  @not_executable [AST.Bool, AST.Dot, AST.Nil, AST.Number, AST.String]

  @unsupported_keywords [:block, :break, :continue, :define, :template, :with]

  @doc """
  Parses `tokens`, lexed from `source`, into a node list.

  Raises `Tend.Template.ParseError`.
  """
  @spec parse(binary(), [Lexer.token()]) :: [AST.tree_node()]
  def parse(source, tokens) do
    {nodes, _state} = top_level(%{source: source, tokens: tokens, vars: ["$"]}, [])
    nodes
  end

  ## The top-level node list

  defp top_level(state, acc) do
    case peek(state) do
      {:eof, _, _} ->
        {Enum.reverse(acc), state}

      _ ->
        case text_or_action(state) do
          {{:control, kind, token}, _state} -> raise stray_control(state, kind, token)
          {node, state} -> top_level(state, [node | acc])
        end
    end
  end

  defp stray_control(state, :else_if, token), do: stray_control(state, :else, token)

  defp stray_control(state, kind, token),
    do: error(state, token, "unexpected {{#{kind}}}")

  # A nested node list, which ends at {{else}} or {{end}} rather than at EOF.
  defp item_list(state), do: item_list(state, [])

  defp item_list(state, acc) do
    case peek_non_space(state) do
      {:eof, _, offset} ->
        raise ParseError.new(state.source, offset, "", "unexpected EOF")

      _ ->
        case text_or_action(state) do
          {{:control, kind, token}, state} -> {Enum.reverse(acc), {kind, token}, state}
          {node, state} -> item_list(state, [node | acc])
        end
    end
  end

  defp text_or_action(state) do
    {token, state} = advance_non_space(state)

    case token do
      {:text, text, offset} -> {%AST.Text{text: text, offset: offset}, state}
      {:left_delim, _, _} -> action(state)
      _ -> raise unexpected(state, token, "input")
    end
  end

  ## Actions

  defp action(state) do
    {token, advanced} = advance_non_space(state)

    case token do
      {:keyword, :else, _} -> else_control(advanced, token)
      {:keyword, :end, _} -> end_control(advanced, token)
      {:keyword, :if, _} -> if_control(advanced)
      {:keyword, :range, _} -> range_control(advanced)
      {:keyword, word, _} when word in @unsupported_keywords -> raise unsupported(state, token)
      _ -> plain_action(skip_spaces(state))
    end
  end

  defp plain_action(state) do
    {pipeline, state} = pipeline(state, "command", :right_delim)
    {%AST.Action{pipeline: pipeline, offset: pipeline.offset}, state}
  end

  defp end_control(state, token) do
    {{:control, :end, token}, expect(state, :right_delim, "end")}
  end

  # `{{else if .B}}` is left half-consumed on purpose: Go rewrites it to
  # `{{else}}{{if .B}}`, and the enclosing {{if}} picks the `if` back up.
  defp else_control(state, token) do
    case peek_non_space(state) do
      {:keyword, :if, _} -> {{:control, :else_if, token}, skip_spaces(state)}
      _ -> {{:control, :else, token}, expect(state, :right_delim, "else")}
    end
  end

  defp if_control(state) do
    {pipeline, body, else_body, state} = control(state, "if", true)

    {%AST.If{pipeline: pipeline, body: body, else_body: else_body, offset: pipeline.offset},
     state}
  end

  defp range_control(state) do
    {pipeline, body, else_body, state} = control(state, "range", false)

    {%AST.Range{pipeline: pipeline, body: body, else_body: else_body, offset: pipeline.offset},
     state}
  end

  defp control(state, context, else_if?) do
    scope = state.vars
    {pipeline, state} = pipeline(state, context, :right_delim)
    {body, stop, state} = item_list(state)
    {else_body, state} = control_else(state, stop, else_if?)
    {pipeline, body, else_body, %{state | vars: scope}}
  end

  defp control_else(state, {:end, _token}, _else_if?), do: {nil, state}

  defp control_else(state, {:else_if, _token}, true) do
    # Consume the `if` that {{else if}} left behind and nest an {{if}} in the
    # else branch. One {{end}} closes the whole chain, as in Go.
    {_if, state} = advance_non_space(state)
    {nested, state} = if_control(state)
    {[nested], state}
  end

  defp control_else(state, _stop, _else_if?) do
    {else_body, stop, state} = item_list(state)

    case stop do
      {:end, _token} -> {else_body, state}
      {kind, token} -> raise error(state, token, "expected end; found {{#{kind}}}")
    end
  end

  defp unsupported(state, {:keyword, word, _} = token) do
    error(state, token, "unsupported action {{#{word}}}")
  end

  ## Pipelines

  defp pipeline(state, context, terminator) do
    offset = offset(peek_non_space(state))
    {decls, assign?, state} = declarations(state, context, [])
    {commands, state} = commands(state, context, terminator, [])
    check_pipeline(state, commands, context, offset)

    {%AST.Pipeline{decls: decls, assign?: assign?, commands: commands, offset: offset}, state}
  end

  # `$x := ...`, `$x = ...` and `range`'s `$i, $o := ...`.
  defp declarations(state, context, acc) do
    case peek_non_space(state) do
      {:variable, name, offset} ->
        {_var, advanced} = advance_non_space(state)
        declaration(state, advanced, context, acc, name, offset)

      _ ->
        {Enum.reverse(acc), false, state}
    end
  end

  defp declaration(state, advanced, context, acc, name, offset) do
    decl = %AST.Variable{name: name, path: [], offset: offset}

    case peek_non_space(advanced) do
      {:assign, _, _} ->
        {Enum.reverse([decl | acc]), true, declare(consume_non_space(advanced), name)}

      {:declare, _, _} ->
        {Enum.reverse([decl | acc]), false, declare(consume_non_space(advanced), name)}

      {:char, ",", _} ->
        more_declarations(
          declare(consume_non_space(advanced), name),
          context,
          [decl | acc],
          offset
        )

      # Not a declaration after all: the variable is an argument. Rewind to
      # before it, keeping whatever earlier declarations already bound.
      _ ->
        {Enum.reverse(acc), false, state}
    end
  end

  defp more_declarations(state, "range", acc, _offset) when length(acc) < 2 do
    case peek_non_space(state) do
      {kind, _, _} when kind in [:variable, :right_delim, :right_paren] ->
        declarations(state, "range", acc)

      token ->
        raise error(state, token, "range can only initialize variables")
    end
  end

  defp more_declarations(state, context, _acc, offset) do
    raise ParseError.new(state.source, offset, "", "too many declarations in #{context}")
  end

  defp declare(state, name), do: %{state | vars: [name | state.vars]}

  defp commands(state, context, terminator, acc) do
    {token, advanced} = advance_non_space(state)

    case token do
      {^terminator, _, _} ->
        {Enum.reverse(acc), advanced}

      {kind, _, _} when kind in @operand_starts ->
        {command, state} = command(skip_spaces(state))
        commands(state, context, terminator, [command | acc])

      _ ->
        raise unexpected(state, token, context)
    end
  end

  defp check_pipeline(state, [], context, offset) do
    raise ParseError.new(state.source, offset, "", "missing value for #{context}")
  end

  defp check_pipeline(state, [_first | rest], _context, _offset) do
    rest
    |> Enum.with_index(2)
    |> Enum.each(fn {command, stage} ->
      head = hd(command.args)

      if head.__struct__ in @not_executable do
        raise ParseError.new(
                state.source,
                head.offset,
                node_text(head),
                "non executable command in pipeline stage #{stage}"
              )
      end
    end)
  end

  ## Commands

  defp command(state) do
    state = skip_spaces(state)
    command(state, [], offset(peek(state)))
  end

  defp command(state, args, offset) do
    state = skip_spaces(state)
    {operand, state} = operand(state)
    args = if operand, do: [operand | args], else: args
    {token, advanced} = advance(state)

    case token do
      {:space, _, _} -> command(advanced, args, offset)
      {:right_delim, _, _} -> {finish_command(state, args, offset), state}
      {:right_paren, _, _} -> {finish_command(state, args, offset), state}
      {:pipe, _, _} -> {finish_command(state, args, offset), advanced}
      _ -> raise unexpected(state, token, "operand")
    end
  end

  defp finish_command(state, [], offset) do
    raise ParseError.new(state.source, offset, "", "empty command")
  end

  defp finish_command(_state, args, offset),
    do: %AST.Command{args: Enum.reverse(args), offset: offset}

  ## Operands

  defp operand(state) do
    case term(state) do
      {nil, state} -> {nil, state}
      {node, state} -> chain(state, node)
    end
  end

  # `.A` followed immediately (no space) by more `.B` tokens is one chain.
  defp chain(state, node) do
    case peek(state) do
      {:field, _, _} ->
        {names, state} = chain_names(state, [])
        {extend(state, node, names), state}

      _ ->
        {node, state}
    end
  end

  defp chain_names(state, acc) do
    case peek(state) do
      {:field, name, _} ->
        {_token, state} = advance(state)
        chain_names(state, [name | acc])

      _ ->
        {Enum.reverse(acc), state}
    end
  end

  defp extend(_state, %AST.Field{} = node, names), do: %{node | path: node.path ++ names}
  defp extend(_state, %AST.Variable{} = node, names), do: %{node | path: node.path ++ names}

  # Go's `operand()` keeps a `ChainNode` when the head of a chain is a
  # function name (`{{len.A}}`) or a parenthesised pipeline (`{{(.A).B}}`),
  # and there is no `ChainNode` in this subset -- see `Tend.Template`'s "What
  # is not, and why". Both are refused by name rather than mis-parsed.
  defp extend(state, %AST.Pipeline{} = node, names),
    do: raise(unsupported_chain(state, node, names, "a parenthesized pipeline"))

  defp extend(state, %AST.Identifier{name: name} = node, names),
    do: raise(unsupported_chain(state, node, names, ~s(the function name "#{name}")))

  # Go's own message, for the terms Go also refuses a chain on.
  defp extend(state, node, names) do
    raise ParseError.new(
            state.source,
            node.offset,
            "." <> Enum.join(names, "."),
            ~s(unexpected . after term "#{node_text(node)}")
          )
  end

  defp unsupported_chain(state, node, names, head) do
    ParseError.new(
      state.source,
      node.offset,
      "." <> Enum.join(names, "."),
      "unsupported field chain on #{head}"
    )
  end

  defp term(state) do
    {token, advanced} = advance(state)

    case token do
      {:identifier, name, offset} ->
        {%AST.Identifier{name: name, offset: offset}, advanced}

      {:dot, _, offset} ->
        {%AST.Dot{offset: offset}, advanced}

      {:nil_literal, _, offset} ->
        {%AST.Nil{offset: offset}, advanced}

      {:variable, name, offset} ->
        {use_var(state, name, offset), advanced}

      {:field, name, offset} ->
        {%AST.Field{path: [name], offset: offset}, advanced}

      {:bool, value, offset} ->
        {%AST.Bool{value: value, offset: offset}, advanced}

      {:string, {value, text}, offset} ->
        {%AST.String{value: value, text: text, offset: offset}, advanced}

      {:number, text, offset} ->
        {number(state, text, offset), advanced}

      {:char_constant, text, offset} ->
        {character(state, text, offset), advanced}

      {:left_paren, _, _} ->
        pipeline(advanced, "parenthesized pipeline", :right_paren)

      _ ->
        {nil, state}
    end
  end

  defp use_var(state, name, offset) do
    if name in state.vars do
      %AST.Variable{name: name, path: [], offset: offset}
    else
      raise ParseError.new(state.source, offset, name, ~s(undefined variable "#{name}"))
    end
  end

  ## Numeric literals

  defp number(state, text, offset) do
    case number_value(text) do
      {:ok, value} -> %AST.Number{value: value, text: text, offset: offset}
      :error -> raise illegal_number(state, text, offset)
    end
  end

  defp character(state, text, offset) do
    body = binary_part(text, 1, byte_size(text) - 2)

    case Lexer.unescape(state.source, offset, body) do
      <<code::utf8>> -> %AST.Number{value: code, text: text, offset: offset}
      <<byte>> -> %AST.Number{value: byte, text: text, offset: offset}
      _ -> raise illegal_number(state, text, offset)
    end
  end

  defp illegal_number(state, text, offset),
    do: ParseError.new(state.source, offset, text, ~s(illegal number syntax: "#{text}"))

  defp number_value(text) do
    clean = String.replace(text, "_", "")

    # Go supports imaginary literals; a prompt has no use for one, and the
    # renderer would have nothing to do with the value.
    if clean == "" or String.ends_with?(clean, "i") do
      :error
    else
      signed(clean)
    end
  end

  defp signed("+" <> rest), do: magnitude(rest)

  defp signed("-" <> rest) do
    case magnitude(rest) do
      {:ok, value} -> {:ok, -value}
      :error -> :error
    end
  end

  defp signed(text), do: magnitude(text)

  defp magnitude(""), do: :error
  defp magnitude("0"), do: {:ok, 0}
  defp magnitude(<<"0", base, rest::binary>>) when base in ~c"xX", do: integer(rest, 16)
  defp magnitude(<<"0", base, rest::binary>>) when base in ~c"oO", do: integer(rest, 8)
  defp magnitude(<<"0", base, rest::binary>>) when base in ~c"bB", do: integer(rest, 2)

  defp magnitude(text) do
    cond do
      String.contains?(text, [".", "e", "E", "p", "P"]) -> float(text)
      String.starts_with?(text, "0") -> integer(binary_part(text, 1, byte_size(text) - 1), 8)
      true -> integer(text, 10)
    end
  end

  defp integer("", _base), do: :error

  defp integer(text, base) do
    case Integer.parse(text, base) do
      {value, ""} -> {:ok, value}
      _ -> :error
    end
  end

  # Go accepts `.5` and `1.`; Elixir's Float.parse/1 wants a digit on both
  # sides of the point.
  defp float(text) do
    text = if String.starts_with?(text, "."), do: "0" <> text, else: text
    text = if String.ends_with?(text, "."), do: text <> "0", else: text

    case Float.parse(text) do
      {value, ""} -> {:ok, value}
      _ -> :error
    end
  end

  ## Token-stream helpers

  defp peek(%{tokens: [token | _]}), do: token

  defp peek_non_space(%{tokens: [{:space, _, _} | rest]} = state),
    do: peek_non_space(%{state | tokens: rest})

  defp peek_non_space(%{tokens: [token | _]}), do: token

  defp skip_spaces(%{tokens: [{:space, _, _} | rest]} = state),
    do: skip_spaces(%{state | tokens: rest})

  defp skip_spaces(state), do: state

  # The EOF sentinel is never consumed, so the stream can always be peeked.
  defp advance(%{tokens: [{:eof, _, _}]} = state), do: {peek(state), state}
  defp advance(%{tokens: [token | rest]} = state), do: {token, %{state | tokens: rest}}

  defp advance_non_space(state), do: advance(skip_spaces(state))

  defp consume_non_space(state), do: elem(advance_non_space(state), 1)

  defp expect(state, type, context) do
    {token, advanced} = advance_non_space(state)

    case token do
      {^type, _, _} -> advanced
      _ -> raise unexpected(state, token, context)
    end
  end

  defp offset({_type, _value, offset}), do: offset

  defp unexpected(state, token, context) do
    error(state, token, "unexpected #{Lexer.describe(token)} in #{context}")
  end

  defp error(state, token, detail),
    do: ParseError.new(state.source, offset(token), Lexer.lexeme(token), detail)

  defp node_text(%AST.String{text: text}), do: text
  defp node_text(%AST.Number{text: text}), do: text
  defp node_text(%AST.Bool{value: value}), do: to_string(value)
  defp node_text(%AST.Dot{}), do: "."
  defp node_text(%AST.Nil{}), do: "nil"
  defp node_text(%AST.Field{path: path}), do: "." <> Enum.join(path, ".")
  defp node_text(%AST.Identifier{name: name}), do: name

  defp node_text(%AST.Variable{name: name, path: []}), do: name

  defp node_text(%AST.Variable{name: name, path: path}),
    do: name <> "." <> Enum.join(path, ".")
end
