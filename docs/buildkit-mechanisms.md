# 🧱 BuildKit の仕組み（学習メモ）

このプロジェクト（`btr`）で実際に使った BuildKit の機能を、**該当ソースコードへのリンク付き**でまとめた学習メモ。
GitHub Actions の [`buildkit-demo`](../.github/workflows/buildkit-demo.yml) ワークフローを実行すると、
このメモに加えて「依存グラフ図」「並列実行・キャッシュヒットの実ログ」がジョブサマリーに出力される。

> 📌 コードへのリンクはコミット [`4fcb094`](https://github.com/sakurai-ryo/buildkit-task-runner/tree/4fcb094) に固定している（行番号がずれないように）。
> 最新版を見たい場合は同じファイルを `main` ブランチで開くこと。

---

## 0. そもそも BuildKit / LLB とは

[BuildKit](https://github.com/moby/buildkit) は Docker のビルドエンジン。普段は `docker build` の裏側で
Dockerfile を解釈して動くが、その**実行モデルの本体は LLB（Low-Level Build definition）**という中間表現にある。

- **LLB は「ビルド処理の DAG（有向非巡回グラフ）」**。各ノード（頂点）が「あるファイルシステム上でコマンドを1つ実行する」「イメージを取得する」「ローカルからファイルを持ってくる」といった操作を表す。
- LLB は protobuf にシリアライズでき、buildkitd（デーモン）に送って実行（**Solve**）する。
- よく「**LLB は Dockerfile に対する LLVM IR**」と例えられる。Dockerfile はあくまでフロントエンドの1つで、Go から LLB を直接組み立てることもできる。`btr` はまさにそれをやっている。

BuildKit がうれしいのは、この DAG を見て **(1) 依存のない枝を自動で並列実行** し、**(2) 各頂点を入力内容のハッシュでキャッシュ** してくれる点。`btr` は「タスクを LLB の DAG に変換する」ことに専念し、並列・キャッシュ・依存実行はすべてエンジンに任せている。

---

## 1. 全体像：tasks.yaml が実行されるまで

```
tasks.yaml
   │  ① config.Load        … YAML を読んで検証
   ▼
 Config (Go の構造体)
   │  ② graph.Resolve      … 依存を辿り、未定義参照・循環を検出
   ▼
 検証済みの依存グラフ
   │  ③ llbgen.Builder.State … 各タスクを llb.State の DAG に変換
   ▼
 llb.State
   │  ④ State.Marshal      … protobuf 定義 (llb.Definition) に変換
   ▼
 llb.Definition
   │  ⑤ client.Solve       … buildkitd に送って実行、進捗を受信
   ▼
 buildkitd が DAG を並列＆キャッシュ付きで実行
```

この①〜⑤の流れは [`cmd/btr/main.go` の `newRunCmd`](https://github.com/sakurai-ryo/buildkit-task-runner/blob/4fcb094/cmd/btr/main.go#L35-L71) にそのまま書かれている。

```go
cfg, err := config.Load(file)            // ①
if err := graph.Resolve(cfg, target); …  // ②
builder := llbgen.New(cfg)
st, err := builder.State(target)         // ③
return runner.Run(ctx, addr, platform, st, builder.LocalMounts()) // ④⑤ は runner 内
```

---

## 2. タスク定義の構造 (`internal/config`)

タスクは [`config.Task`](https://github.com/sakurai-ryo/buildkit-task-runner/blob/4fcb094/internal/config/config.go#L16-L25) という単純な構造体。

```go
type Task struct {
    Image  string            `yaml:"image"`
    Cmds   []string          `yaml:"cmds"`
    Deps   []string          `yaml:"deps"`
    Env    map[string]string `yaml:"env"`
    Dir    string            `yaml:"dir"`
    Source string            `yaml:"source"` // ローカルディレクトリを read-only マウント
    Caches []string          `yaml:"caches"` // 永続キャッシュにするコンテナ内パス
}
```

[`config.Load`](https://github.com/sakurai-ryo/buildkit-task-runner/blob/4fcb094/internal/config/config.go#L26-L41) で YAML を読み込み、`image`/`cmds` 必須・`deps` 参照先の存在チェックを行う。
ここはまだ BuildKit と無関係な「ただの設定読み込み」。

---

## 3. 依存解決と循環検出 (`internal/graph`)

[`graph.Resolve`](https://github.com/sakurai-ryo/buildkit-task-runner/blob/4fcb094/internal/graph/graph.go#L23-L48) は、ターゲットから `deps` を DFS で辿り、

- **未定義タスクの参照**（typo など）
- **循環依存**（`a → b → a`）

を検出する。循環検出は **3色マーキング**（white=未訪問 / gray=訪問中 / black=完了）で行い、gray のノードに再到達したら循環。

```go
case gray:
    return fmt.Errorf("cyclic dependency detected: %v", append(path, name))
```

> 💡 実際の DAG 構築は次節の `llbgen` が再帰＋メモ化で行うため、厳密なトポロジカルソートは不要。
> `Resolve` はあくまで「壊れたグラフを早期に弾く」ための健全性チェック。

なお [`graph.Mermaid`](https://github.com/sakurai-ryo/buildkit-task-runner/blob/4fcb094/internal/graph/graph.go#L53) は同じグラフを Mermaid 図として出力する関数で、`btr graph` サブコマンドが使う（ワークフローのサマリーに図が出るのはこれ）。

---

## 4. ここからが本題：タスク → LLB 変換 (`internal/llbgen`)

[`Builder.State`](https://github.com/sakurai-ryo/buildkit-task-runner/blob/4fcb094/internal/llbgen/llbgen.go#L47-L105) が、1つのタスクを 1つの `llb.State`（＝ DAG の部分木）に変換する中心。
ここで使っている BuildKit の機能を順に見ていく。

### 4-1. ベースイメージとイメージ設定の解決（落とし穴あり）

```go
st := llb.Image(t.Image, llb.WithMetaResolver(imagemetaresolver.Default()))
```
[llbgen.go#L55](https://github.com/sakurai-ryo/buildkit-task-runner/blob/4fcb094/internal/llbgen/llbgen.go#L53-L55)

- `llb.Image()` はイメージを DAG の起点ノードにする。
- ⚠️ **落とし穴**：`llb.Image()` だけだと**イメージの rootfs しか取り込まれず、イメージ設定（`PATH` などの ENV、WORKDIR、ENTRYPOINT）は適用されない**。そのため golang イメージを使っても `go: executable file not found in $PATH`（`go` は `/usr/local/go/bin` にあるが PATH に入っていない）になる。
- `llb.WithMetaResolver(imagemetaresolver.Default())` を渡すと、レジストリからイメージ config を解決し、`PATH` 等の ENV が State に反映される。`docker run` 相当の「イメージの設定が効いた状態」になる。

### 4-2. 作業ディレクトリと環境変数

```go
workdir := t.Dir
if workdir == "" && t.Source != "" {
    workdir = defaultWorkdir // "/src"
}
if workdir != "" { st = st.Dir(workdir) }
for k, v := range t.Env { st = st.AddEnv(k, v) }
```
[llbgen.go#L57-L66](https://github.com/sakurai-ryo/buildkit-task-runner/blob/4fcb094/internal/llbgen/llbgen.go#L57-L66)

`State.Dir()` / `State.AddEnv()` はいずれも**新しい State を返す**（イミュータブル）。`source` がある場合は既定で `/src` を作業ディレクトリにする。

### 4-3. コマンドのチェーン実行

```go
for i, cmd := range t.Cmds {
    run := st.Run(llb.Shlex(cmd), llb.WithCustomNamef("[%s] %s", name, cmd))
    …
    st = run.Root()   // 直前コマンドの結果 rootfs の上に次のコマンドを積む
}
```
[llbgen.go#L82-L101](https://github.com/sakurai-ryo/buildkit-task-runner/blob/4fcb094/internal/llbgen/llbgen.go#L82-L101)

- `State.Run()` は実行頂点（`ExecState`）を作る。`llb.Shlex(cmd)` は文字列をシェル風に分割する（`sh -c '...'` のように引用符も解釈する）。
- `run.Root()` でそのコマンド実行後の rootfs を次の State として取り出し、次のコマンドへ積み上げる。これで `cmds` が**順番に**実行される。
- `llb.WithCustomNamef` は進捗表示に出る頂点名（`[build] go build …` のような表示はこれ）。

### 4-4. 依存を「順序エッジ」として表現

```go
if i == 0 { // 依存は最初のコマンドにだけマウント
    for _, dep := range t.Deps {
        ds, err := b.State(dep)          // 依存タスクの State を再帰生成
        run.AddMount("/.btr/deps/"+dep, ds, llb.Readonly)
    }
}
```
[llbgen.go#L91-L99](https://github.com/sakurai-ryo/buildkit-task-runner/blob/4fcb094/internal/llbgen/llbgen.go#L91-L99)

- 依存タスクの結果ステートを `/.btr/deps/<dep>` に **read-only マウント** する。
- これは「データを共有したい」わけではなく、**「依存が先に完了していること」を LLB の辺として表現する**ためのテクニック。あるノードが別ノードの出力をマウントすると、BuildKit はマウント元を先に解決する。＝ **ordering edge**。
- マウント先（`/.btr/deps/...`）はタスク本体からは普通使わない慣習パス。

### 4-5. メモ化による「共有依存を1回だけ」＋自動並列

```go
func (b *Builder) State(name string) (llb.State, error) {
    if s, ok := b.memo[name]; ok { return s, nil } // ← メモ化
    …
    b.memo[name] = st
    return st, nil
}
```
[llbgen.go#L47-L50, L103](https://github.com/sakurai-ryo/buildkit-task-runner/blob/4fcb094/internal/llbgen/llbgen.go#L47-L50)

- `Builder.memo`（`map[string]llb.State`）で変換結果をキャッシュ。複数タスクが同じ依存（例：`deps`）を参照しても、その State は**1つだけ**生成される＝ DAG 上でも 1 頂点に収束し、**1回だけ実行**される。
- 互いに依存しない枝（例：`fmt` / `vet` / `build` / `test`）は DAG 上で並列なので、**BuildKit が自動で並列実行**する。`btr` 側に並列制御コードは一切ない。

### 4-6. ローカルソースのマウント（`llb.Local`）

```go
srcState = llb.Local(mountName,
    llb.SharedKeyHint(mountName),
    llb.ExcludePatterns([]string{".git", "btr"}),
    llb.WithCustomNamef("local://%s", t.Source),
)
…
run.AddMount(workdir, srcState, llb.Readonly) // 作業ディレクトリに read-only マウント
```
[llbgen.go#L75-L86](https://github.com/sakurai-ryo/buildkit-task-runner/blob/4fcb094/internal/llbgen/llbgen.go#L75-L86)

- `llb.Local(name)` は「**クライアント（btr 側）のローカルディレクトリ**」を DAG の入力にする頂点。`name` はクライアントが渡すマウント名のキーになる。
- 実体のディレクトリは runner 側で `SolveOpt.LocalMounts` として渡す（→ 5-2）。
- `ExcludePatterns` で `.git` やビルド済み `btr` バイナリを除外。これは転送量を減らすだけでなく、**キャッシュキーの安定**にも効く（無関係なファイルが変わっても再ビルドされない）。
- [`registerSource`](https://github.com/sakurai-ryo/buildkit-task-runner/blob/4fcb094/internal/llbgen/llbgen.go#L109-L117) で、同じ `source` を複数タスクが指定しても 1 マウントに集約している。

### 4-7. 永続キャッシュマウント（`AsPersistentCacheDir`）

```go
for _, cachePath := range t.Caches {
    run.AddMount(cachePath, llb.Scratch(),
        llb.AsPersistentCacheDir(cachePath, llb.CacheMountShared))
}
```
[llbgen.go#L87-L90](https://github.com/sakurai-ryo/buildkit-task-runner/blob/4fcb094/internal/llbgen/llbgen.go#L87-L90)

- 指定パスを「**実行をまたいで残る共有キャッシュ領域**」としてマウントする（Dockerfile の `RUN --mount=type=cache` と同じ仕組み）。
- `btr` ではこれで `/go/pkg/mod`（モジュールキャッシュ）と `/root/.cache/go-build`（ビルドキャッシュ）を共有し、タスク間・実行間で **Go の再ダウンロード／再コンパイルを回避**している。これが無いと毎タスクが空のコンテナで巨大な依存ツリーを取得・コンパイルし直して非現実的に遅くなる。
- ⚠️ 注意：永続キャッシュの**中身**はキャッシュキーに含まれない（後述の 5-3 のコンテンツキャッシュとは別物）。あくまで「速くするための作業領域」。

---

## 5. Solve：buildkitd に投げて実行する (`internal/runner`)

[`runner.Run`](https://github.com/sakurai-ryo/buildkit-task-runner/blob/4fcb094/internal/runner/runner.go#L48-L86) が実行の本体。

### 5-1. 接続と Marshal

```go
c, err := client.New(ctx, addr)        // buildkitd へ接続
def, err := st.Marshal(ctx, platform)  // llb.State → llb.Definition (protobuf)
```
[runner.go#L48-L58](https://github.com/sakurai-ryo/buildkit-task-runner/blob/4fcb094/internal/runner/runner.go#L48-L58)

- 接続先は [`Address`](https://github.com/sakurai-ryo/buildkit-task-runner/blob/4fcb094/internal/runner/runner.go#L19-L27)（`--addr` > `BUILDKIT_HOST` > 既定 unix ソケット）で解決。
- `Marshal` の引数 [`Platform`](https://github.com/sakurai-ryo/buildkit-task-runner/blob/4fcb094/internal/runner/runner.go#L30-L44) はターゲット OS/アーキ。`--platform` 未指定ならホストの `GOARCH` から決める。

### 5-2. ローカルマウントの受け渡し（fsutil）

```go
for name, dir := range localMounts {
    fs, err := fsutil.NewFS(dir)
    mounts[name] = fs
}
solveOpt.LocalMounts = mounts
```
[runner.go#L60-L71](https://github.com/sakurai-ryo/buildkit-task-runner/blob/4fcb094/internal/runner/runner.go#L60-L71)

- 4-6 の `llb.Local(name)` に対応する実ディレクトリを、`fsutil.FS` にして `SolveOpt.LocalMounts[name]` に渡す。
- buildkitd は実行中、この `fsutil.FS` から（gRPC セッション経由で）必要なファイルを取り寄せる。＝ **ローカルのコードがコンテナに入る**のはこの仕組み。

### 5-3. Solve と進捗ストリーミング

```go
ch := make(chan *client.SolveStatus)
go printStatus(ch)
_, err = c.Solve(ctx, def, solveOpt, ch)
```
[runner.go#L73-L86](https://github.com/sakurai-ryo/buildkit-task-runner/blob/4fcb094/internal/runner/runner.go#L73-L86)

- `Client.Solve` が DAG を実行する。第4引数の `statusChan` に進捗（`SolveStatus`）が流れてくる。
- [`printStatus`](https://github.com/sakurai-ryo/buildkit-task-runner/blob/4fcb094/internal/runner/runner.go#L89-L111) が各頂点の開始（`▶`）／完了（`✔`）と、`v.Cached` を見て `cached` 表示、`v.Logs` の出力を整形している。

### コンテンツアドレッサブルキャッシュ

LLB の**各頂点は、その入力内容のハッシュ**（ベースイメージ、マウントしたソースの内容、コマンド文字列、依存の結果…）で識別される。入力が前回と同じなら頂点は再実行されず **キャッシュヒット** する。

ワークフローで `btr run ci` を2回実行すると、2回目はタスク頂点がすべて `(cached)` になる。
逆に、ソース（`llb.Local`）の内容が1バイトでも変わるとそのハッシュが変わり、依存する下流の頂点も再実行される。
（このため、ワークフローではログを作業ディレクトリ外＝ `$RUNNER_TEMP` に出して、cold/warm 間でソースを不変に保っている。）

---

## 6. デバッグログで動きを追う

`--debug`（または `BTR_DEBUG=1`）を付けると、上記①〜⑤の各段階を stderr に実況する。
特に `llbgen` のログは**インデントで再帰の深さ**を示し、共有依存が**メモ化で1回だけ**構築される様子も見える。

`./btr run all -f examples/tasks.yaml --debug` の抜粋（`all → {lint, build} → deps` の例）:

```
[debug] graph: resolving dependencies from "all"
[debug] graph: all deps=[lint build]
[debug] graph:   lint deps=[deps]
[debug] graph:     deps deps=[]
[debug] graph:   build deps=[deps]
[debug] graph:     deps (already checked)         ← DFS で deps に再到達（循環ではない）
[debug] llbgen: task "all": building LLB (image=alpine:3.20, cmds=1, deps=[lint build])
[debug] llbgen:   task "lint": building LLB ...
[debug] llbgen:     task "deps": building LLB ... ← deps を初めて構築
[debug] llbgen:   task "build": building LLB ...
[debug] llbgen:     task "deps": reusing memoized state (shared dependency, built once)  ← ここがメモ化！
[debug] runner: marshaled LLB definition (8 operations in the graph)
[debug] runner: solving (buildkitd runs the graph with parallelism and caching)...
[debug] runner: solve completed
```

ログの出力箇所はコード側の `debug.Logf(...)` 呼び出しと対応している（`internal/debug`、各パッケージ内）。

## 7. 動かして確かめる

### GitHub Actions（おすすめ）
[`buildkit-demo`](../.github/workflows/buildkit-demo.yml) を実行すると、ジョブサマリーに
**このメモ＋依存グラフ図（Mermaid）＋ cold/warm 実行ログ（キャッシュヒット数）**が出力される。

### ローカル
```sh
# buildkitd を TCP で起動
docker run -d --name buildkitd --privileged -p 1234:1234 \
  moby/buildkit:latest --addr tcp://0.0.0.0:1234
export BUILDKIT_HOST=tcp://127.0.0.1:1234

go build -o btr ./cmd/btr
./btr graph ci          # 依存グラフを Mermaid で出力
./btr run ci            # fmt + vet + build + test を並列実行
./btr run ci            # 2回目はキャッシュでほぼ即時
```

タスク定義の実物は [`tasks.yaml`](../tasks.yaml) を参照。

---

## 8. 今回は使っていない（ロードマップ）

- 成果物のローカル出力（`SolveOpt.Exports` / `ExportEntry`）— 今はコンテナ内で完結し、バイナリ等は取り出していない。
- progressui による TUI 進捗表示 — 今は素朴な行出力。
- キャッシュの import/export（レジストリ等への外部キャッシュ）— 今はデーモンローカルのキャッシュのみ。
