defmodule Coldstart.MixProject do
  use Mix.Project

  def project do
    [
      app: :coldstart,
      version: "0.1.0",
      elixir: "~> 1.18",
      start_permanent: false,
      deps: deps(),
      releases: releases()
    ]
  end

  def application do
    [
      # No :logger: nothing in the one-shot path logs, and every
      # application in the boot list costs module loads at start.
      extra_applications: [],
      mod: {Coldstart, []}
    ]
  end

  defp deps do
    [
      {:exqlite, "0.40.0"},
      # Build-time only. As a runtime dep it drags req/finch/mint/ssl/
      # crypto/public_key into the release and starts them all at boot.
      {:burrito, "1.6.0", runtime: false}
    ]
  end

  defp releases do
    [
      coldstart: [
        include_executables_for: [:unix],
        strip_beams: true,
        # Only what the one-shots need. :elixir pulls in the stdlib; :eex,
        # :mix, :iex, :ex_unit are left out on purpose.
        applications: [
          kernel: :permanent,
          stdlib: :permanent,
          elixir: :permanent,
          exqlite: :permanent,
          coldstart: :permanent
        ],
        steps: [:assemble, &Burrito.wrap/1],
        burrito: [
          targets: [
            macos_arm64: [os: :darwin, cpu: :aarch64]
          ]
        ]
      ]
    ]
  end
end
