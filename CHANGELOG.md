# Changelog

## [0.3.0](https://github.com/jwstover/tend/compare/v0.2.0...v0.3.0) (2026-09-01)


### ⚠ BREAKING CHANGES

* **cli:** `tend projects use` and `tend ls --all` are removed. `tend add` no longer honours the project the TUI last selected; use -p.
* **cli:** `tend ls` no longer dumps every task. It shows the project new captures land in, so it agrees with what the TUI displays against the same database; `--all` restores the old behaviour.
* the MCP tool set_task_project is replaced by set_task_tags, and the TUI's P binding moves to T. Migration 00007 drops tasks.project; its Down restores only each task's first tag.

### Features

* add task events, log entries, and a standup view ([eaf697d](https://github.com/jwstover/tend/commit/eaf697d60aebd237fa094966d923dec10b890c82))
* add task events, log entries, and a standup view ([e2cb117](https://github.com/jwstover/tend/commit/e2cb1174cd266980ff741403bcccb0cb13fa497e))
* **agent:** report session status via Claude Code hooks ([7004dd0](https://github.com/jwstover/tend/commit/7004dd0056fb32d094b30e6bc8a88e3e65225e17))
* **agent:** run claude inside a dedicated tmux server ([651e9c9](https://github.com/jwstover/tend/commit/651e9c95f8e1bab65ca82565d5f5c5a81e740739))
* auto-name agent sessions from their recap ([534fab7](https://github.com/jwstover/tend/commit/534fab706756df98d412dc82726617de9d33c2f8))
* background and reattach agent sessions ([ea0c5b2](https://github.com/jwstover/tend/commit/ea0c5b229548985e07307c68bd7c765e03de6c5f))
* background and reattach Claude Code sessions via tmux ([c492985](https://github.com/jwstover/tend/commit/c492985894519e31a7355b747ff37f92ca3407d9))
* **cli:** add the tend projects command group ([70ba4ca](https://github.com/jwstover/tend/commit/70ba4ca0ce1248b9630c828aa92c90a02bae7ad0))
* **cli:** keep project state out of the shell ([1a35f77](https://github.com/jwstover/tend/commit/1a35f7710f3b1459acb8c85915b3b969529ccb21))
* detect "working" session status via tmux capture-pane polling ([27f2b74](https://github.com/jwstover/tend/commit/27f2b74f7a773b926d0e49ce452337fbddd6f7da))
* launch and resume Claude Code sessions from a task ([0484d96](https://github.com/jwstover/tend/commit/0484d962d7c06b447793d0bec635637e5d8d0c51))
* log a headless recap after a session ends, scoped to resumed work ([e08ba0d](https://github.com/jwstover/tend/commit/e08ba0da5ea47851a8ca943789a00b7e9fdeb9bf))
* **mcp:** expose projects to a bound session ([bac50c8](https://github.com/jwstover/tend/commit/bac50c84415874e099593c516fdbd82b6e508ba7))
* **mcp:** give launched sessions read/write access to their bound task ([d13e3b1](https://github.com/jwstover/tend/commit/d13e3b196b9120271e8c29fccf6a6ee0395c59b6))
* quick-create tasks from Jira issue URLs ([f77e98b](https://github.com/jwstover/tend/commit/f77e98b5a8ef1e12d5c47456e9575c027235851c))
* quick-create tasks from Jira issue URLs ([4be174f](https://github.com/jwstover/tend/commit/4be174f5edc0b6b759805eae3889b44d8b903e27))
* replace the flat project string with projects and tags ([2da05ec](https://github.com/jwstover/tend/commit/2da05ecd91040356510d280af0e5cc6cda838f55))
* report Claude Code session status via hooks ([eda16b2](https://github.com/jwstover/tend/commit/eda16b22c5f0bd2ec55f567a5a1022305cee5d44))
* report project moves in standup ([dc3b4dd](https://github.com/jwstover/tend/commit/dc3b4ddac0c46445ce010587981f196c246fea1b))
* **store:** add SetSessionEndedIfUnchanged CAS write ([8613ff4](https://github.com/jwstover/tend/commit/8613ff47d66ef8c28092216d66955cfdab15831c))
* toggle Claude recap notes in the standup view ([541cc56](https://github.com/jwstover/tend/commit/541cc5600c13a30425300b53dbbcbae29cc72725))
* **tui:** add the projects column ([136f43b](https://github.com/jwstover/tend/commit/136f43bf8b162013e22e12ed473bb84c6357611d))
* **tui:** collapse tag overflow to a counter ([0fe8e30](https://github.com/jwstover/tend/commit/0fe8e30b803bbbfa825c8160a93ea5fed23a6d19))
* **tui:** delete the selected task with a dd chord ([2cf94db](https://github.com/jwstover/tend/commit/2cf94dbfa2bddc0ee774c080b63686d9c1577af0))
* **tui:** delete the selected task with a dd chord ([5b20897](https://github.com/jwstover/tend/commit/5b2089701e2393e23172e2fe08549550ccbd4fcb))
* **tui:** give sub-tasks their own detail pane ([00950b9](https://github.com/jwstover/tend/commit/00950b9ef133bf9e3d1214618409a0d957dc8145))
* **tui:** let l open the detail pane before focusing it ([4883d20](https://github.com/jwstover/tend/commit/4883d20fd0da6775dcceb99649049336afacaeda))
* **tui:** let the detail pane take focus for scrolling ([29f6015](https://github.com/jwstover/tend/commit/29f6015aec6311eba729092e90af541ad13f8ff5))
* **tui:** let the detail pane take focus for scrolling ([5b9aecc](https://github.com/jwstover/tend/commit/5b9aecc0eacc14d6531278c3efc128a746e513ff))
* **tui:** make sub-tasks first-class in the detail pane ([01c1ce5](https://github.com/jwstover/tend/commit/01c1ce53621b2f8c6da733dc9ac6c25f13788aaf))
* **tui:** name the scoped project in the header ([1a89771](https://github.com/jwstover/tend/commit/1a897710fc3811532357d2dff9b8e046d75387d9))
* **tui:** q closes the current view instead of quitting ([0ce348f](https://github.com/jwstover/tend/commit/0ce348fcb9e1379d83cab946c92762542b43fdc3))
* **tui:** q closes the current view instead of quitting ([b600869](https://github.com/jwstover/tend/commit/b600869e140a4a20b9a4d400027774834b5348fd))
* **tui:** rename a task in place ([567ad38](https://github.com/jwstover/tend/commit/567ad38a54039846e34db84faad23805f1b7071f))
* **tui:** rename a task in place ([f456dd5](https://github.com/jwstover/tend/commit/f456dd536105e15b592f6c5dfb197f75b159427a))
* **tui:** settle recaps owed by backgrounded sessions ([6403319](https://github.com/jwstover/tend/commit/6403319dd7ba3ae21bb5641434a2cbf697e8f149))
* **tui:** show agent session status on task and session rows ([604cb20](https://github.com/jwstover/tend/commit/604cb20a3073cc6f0398f85ec7e4a33bb8ceaf36))
* **tui:** show markdown link titles in link lists ([f25308c](https://github.com/jwstover/tend/commit/f25308cb013975b52f1a33950fb388497915322c))
* **tui:** show markdown link titles in link lists ([97c9093](https://github.com/jwstover/tend/commit/97c9093048e47158045aff513fca079715fc5e1f))
* **tui:** show workflow state on sub-task rows ([2f00a98](https://github.com/jwstover/tend/commit/2f00a986bc8281832845b4dc05626ccc68ac924f))
* **tui:** surface agent session status in the UI ([3afab81](https://github.com/jwstover/tend/commit/3afab81d1d124ceb786c129063a0307373caaf0e))
* warn before quitting while a session recap is still running ([7a7ad19](https://github.com/jwstover/tend/commit/7a7ad1920a476786dc4c0f11ee76ba98b54f3fa9))


### Bug Fixes

* let the poller take a stale "working" status back down ([afe01ab](https://github.com/jwstover/tend/commit/afe01ab508a5218b7519a3f991eee851e0d013c1))
* **tui:** decouple session poller from event loop, broaden self-heal to blocked/starting ([3210a12](https://github.com/jwstover/tend/commit/3210a12eab0c6309b6bc1d8e4ccdeaef0526a260))
* **tui:** decouple session poller from event loop, broaden self-heal to blocked/starting ([6390c9b](https://github.com/jwstover/tend/commit/6390c9bd7f01e16973039d6156ce8629947dd74b))
* **tui:** drop ineffectual assignments flagged by golangci-lint ([3e36e88](https://github.com/jwstover/tend/commit/3e36e88d062d75ede4973b8bb4669ecb6fda978a))
* **tui:** mark dead sessions ended from the poller ([ed9966a](https://github.com/jwstover/tend/commit/ed9966abe9ef02fe25d864448f8a01474a8ed8bd))
* **tui:** mark dead sessions ended from the poller ([fd9463a](https://github.com/jwstover/tend/commit/fd9463a58153a6cce16f78f11cebd0d561c170d2))
* wrap list_subtasks output in an object schema ([42d3b93](https://github.com/jwstover/tend/commit/42d3b93bc40b7f52a940c9808acae6007c7467b7))
* wrap list_subtasks output in an object schema ([d8dd667](https://github.com/jwstover/tend/commit/d8dd66705f6b6db327d51f99c9c5ac177e6d5567))

## [0.2.0](https://github.com/jwstover/tend/compare/v0.1.0...v0.2.0) (2026-06-15)


### Features

* add link picker when a task has multiple links ([#9](https://github.com/jwstover/tend/issues/9)) ([22da8f0](https://github.com/jwstover/tend/commit/22da8f0e9e0ef19f159cac63df0ea70b0e89ec00))

## 0.1.0 (2026-06-12)


### Features

* add version command and automated release pipeline ([e8e13cc](https://github.com/jwstover/tend/commit/e8e13cc85edb2340ccb4151cc83b40aeeb70bd5b))
* add version command and automated release pipeline ([4bd81d8](https://github.com/jwstover/tend/commit/4bd81d8f393743e002e7c29b21db87c0d46b959a))
