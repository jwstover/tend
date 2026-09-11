import Config

# Explicit render-cache cap so BackBreeze does not want :os_mon (see the
# Breeze README). 64 MiB is plenty for a one-screen spike.
config :back_breeze, render_cache_max_memory_bytes: 64 * 1024 * 1024
