# 🧱 BuildKit の仕組み（学習メモ）

このプロジェクト（`btr`）で実際に使った BuildKit の機能を、対応するコードと一緒にまとめた学習用メモ。
GitHub Actions の [`buildkit-demo`](../.github/workflows/buildkit-demo.yml) ワークフローを実行すると、
このメモに加えて「依存グラフ図」「並列実行・キャッシュヒットの実ログ」がジョブサマリーに出力される。

## 全体像

```
tasks.yaml ──parse──▶ config ──build──▶ llb.State グラフ ──Marshal──▶ llb.Definition ──Solve──▶ buildkitd
```

`btr` は各タスクを **LLB（Low-Level Build definition）** という DAG に変換し、buildkitd に投げて実行するだけ。
依存解決・並列実行・キャッシュはすべて BuildKit エンジンに委譲している。LLB は「Dockerfile に対する LLVM IR」に例えられる中間表現。

## 使った機能

### 1. LLB ステートグラフの構築 (`internal/llbgen/llbgen.go`)

各タスクを `llb.Image(image)` を起点に `State.Run(llb.Shlex(cmd))` でチェーンし、1つの `llb.State` を作る。
最終的に `State.Marshal()` で `llb.Definition`（protobuf のグラフ）に変換する。

### 2. イメージ設定の解決 / imagemetaresolver (`llbgen.go`)

`llb.Image()` は **イメージの rootfs しか取り込まず、設定（`PATH` 等の ENV や WORKDIR）は自動適用しない**。
そのままだと golang イメージでも `go: executable file not found in $PATH` になる。
`llb.WithMetaResolver(imagemetaresolver.Default())` を付けてイメージ config を解決し、`PATH` 等を反映させている。

### 3. 依存を「順序エッジ」として表現 (`llbgen.go`)

タスクの依存（`deps`）は、依存タスクの結果ステートを最初のコマンドに **read-only マウント** することで表現する。

```go
run.AddMount("/.btr/deps/"+dep, depState, llb.Readonly)
```

データは共有せず「依存が先に完了していること」だけを保証する純粋な **ordering edge**。これが LLB グラフ上の辺になる。

### 4. メモ化による共有依存の重複排除 + 自動並列化 (`llbgen.go`)

`Builder.memo`（`map[string]llb.State`）で変換結果をキャッシュするため、複数タスクが同じ依存を参照しても
その依存は **1度だけ** ビルドされる。互いに独立した枝は BuildKit が **自動で並列実行** する
（例: `fmt` / `vet` / `build` / `test` が同時に走る）。

### 5. ローカルソースのマウント / llb.Local (`llbgen.go` + `internal/runner/runner.go`)

ホストのコードをコンテナに渡すには `llb.Local(name)` でローカルソースを参照し、
クライアント側で `SolveOpt.LocalMounts`（`fsutil.FS`）として実ディレクトリを buildkitd に公開する。

```go
src := llb.Local(name, llb.ExcludePatterns([]string{".git", "btr"}))
// runner 側:
solveOpt.LocalMounts = map[string]fsutil.FS{name: fs}
```

`btr` ではこれを使ってプロジェクト自身を `/src` に read-only マウントし、`go build` / `go test` を回している。

### 6. 永続キャッシュマウント / AsPersistentCacheDir (`llbgen.go`)

`caches` に指定したパスを、実行をまたいで再利用される共有キャッシュとしてマウントする。

```go
run.AddMount(path, llb.Scratch(), llb.AsPersistentCacheDir(path, llb.CacheMountShared))
```

`/go/pkg/mod`（モジュールキャッシュ）と `/root/.cache/go-build`（ビルドキャッシュ）をこれで共有し、
タスク間・実行間で Go の再ダウンロード／再コンパイルを避けている。

### 7. コンテンツアドレッサブルキャッシュ

LLB の各頂点はその入力内容のハッシュで識別される。入力が変わらなければ再実行されず **キャッシュヒット** する。
2回目の `btr run ci` でほぼ全頂点が `(cached)` になるのはこの仕組み（ワークフローの実ログ参照）。

### 8. Solve と進捗ストリーミング (`runner.go`)

`client.New()` で buildkitd に接続し、`Client.Solve(ctx, def, opt, statusChan)` で実行する。
`statusChan`（`chan *client.SolveStatus`）から頂点の開始／完了・キャッシュ状態・ログを受け取って表示している。

## 今回は使っていない（ロードマップ）

- 成果物のローカル出力（`SolveOpt.Exports`）
- progressui による TUI 進捗表示
- キャッシュの import/export（レジストリ等への外部キャッシュ）
