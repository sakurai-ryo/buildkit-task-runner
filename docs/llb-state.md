# 🔬 `llb.State` 深掘り

[`buildkit-mechanisms.md`](./buildkit-mechanisms.md) の補足。LLB API の中心型 **`llb.State`** が
実際どう作られているかを、BuildKit のソース（このプロジェクトが使う **v0.30.0**）を読みながら解説する。

> 📌 BuildKit 側のリンクはタグ [`v0.30.0`](https://github.com/moby/buildkit/tree/v0.30.0) に固定。
> このプロジェクト側のコードは [`internal/llbgen/llbgen.go`](../internal/llbgen/llbgen.go) を参照。

---

## 一言でいうと

`State` は **「ある出力（ファイルシステム）を作るために必要な操作グラフ」を表す、LLB API の中心型**。
公式コメント（[state.go#L53-L58](https://github.com/moby/buildkit/blob/v0.30.0/client/llb/state.go#L53-L58)）より:

> States are **immutable**, and all operations return a **new state** linked to the previous one. (中略)
> Operations performed on a State are **executed lazily** after the entire state graph is marshalled and sent to the backend.

つまり `State` の核は3つ:

1. **イミュータブル**（操作するたびに新しい State が返る）
2. **グラフを組み立てる**（操作の DAG を表す）
3. **遅延実行**（`Marshal` → `Solve` まで何も走らない）

---

## 1. `State` 構造体の正体（実体は2層）

[state.go#L59-L67](https://github.com/moby/buildkit/blob/v0.30.0/client/llb/state.go#L59-L67):

```go
type State struct {
	out   Output                                          // ① 操作グラフ：この State を生む「頂点」への参照
	prev  *State                                          // ┐
	key   any                                             // ├ ② メタデータチェーン（env, workdir 等）を
	value func(context.Context, *Constraints) (any, error)// ┘   prev でつないだ連結リスト
	opts  []ConstraintsOpt
	async *asyncState
}
```

最重要ポイント：1つの `State` に**性質の違う2つの情報**が同居している。

### ① 操作グラフ（`out Output`）— 「何を実行するか」の DAG
`out` は、この State の中身を生成する**頂点（Vertex）**への参照。`Image` / `Run`(Exec) / `File` / `Local`
などの操作がそれぞれ Vertex になり、`Vertex.Inputs()` が依存する他の Output を返すことで **DAG の辺**ができる
（[state.go#L26-L30](https://github.com/moby/buildkit/blob/v0.30.0/client/llb/state.go#L26-L30)）。

### ② メタデータチェーン（`prev`/`key`/`value`）— 「どんな環境で実行するか」
`Dir`（作業ディレクトリ）や `AddEnv`（環境変数）は、操作グラフを増やさず、
**`key→value` を `prev` でつないだ連結リスト**に積むだけ。

```go
// meta.go: Dir も AddEnv も内部は withValue でチェーンに積むだけ
return s.withValue(keyDir, func(ctx, c) (any, error) { ... })  // meta.go:81
return s.withValue(keyEnv, func(ctx, c) (any, error) { ... })  // meta.go:54
```

値の取り出しは `getValue` が `prev` を遡って一致 `key` を探す
（[state.go#L103-L120](https://github.com/moby/buildkit/blob/v0.30.0/client/llb/state.go#L103-L120)）。見つからなければ `nilValue`。

> 💡 だから `AddEnv` を100回呼んでも実行頂点は増えない。env/workdir は「実行頂点(Exec)を Marshal する瞬間に」
> チェーンを遡って解決され、その Exec のメタとして埋め込まれる。

---

## 2. イミュータビリティ

全メソッドが**新しい `State` を返す**（レシーバが値型 `func (s State)`）。元の State は変わらない。

```go
func (s State) Dir(str string) State { return Dir(str)(s) }              // state.go:331
func (s State) AddEnv(key, value string) State { return AddEnv(...)(s) } // state.go:320
```

このプロジェクトが必ず**代入し直している**のはこのため:

```go
// internal/llbgen/llbgen.go（State の章）
st = st.Dir(workdir)   // ← 戻り値を使わないと捨てられる。再代入が必須
st = st.AddEnv(k, v)
```

---

## 3. 遅延評価（Lazy）

組み立て中は**一切実行されない**。さらに値が固定値ではなく `func(ctx, *Constraints) (any, error)` という
**関数**で保持される点が効いている（struct の `value` フィールド）。これにより:

- プラットフォーム（`Constraints`）に応じて値を変える
- `Async`（[state.go#L122](https://github.com/moby/buildkit/blob/v0.30.0/client/llb/state.go#L122)）で
  「Marshal 時に初めてレジストリへ問い合わせて確定」する遅延解決

ができる。本プロジェクトの `imagemetaresolver` も、この遅延解決に乗って
**Marshal のタイミングでイメージ config（PATH 等）を取得**している。

---

## 4. `Run` と `ExecState`（`State` との関係）

`State.Run` は実行操作を作り **`ExecState`** を返す（[state.go#L288](https://github.com/moby/buildkit/blob/v0.30.0/client/llb/state.go#L288)）:

```go
func (s State) Run(ro ...RunOption) ExecState {
	exec := NewExecOp(...)                   // 実行頂点を生成
	return ExecState{
		State: s.WithOutput(exec.Output()),  // s を「Exec の出力を out に持つ新 State」に
		exec:  exec,
	}
}
```

`ExecState` は `State` を**埋め込み**つつ exec への参照を持つ（[exec.go#L520-L535](https://github.com/moby/buildkit/blob/v0.30.0/client/llb/exec.go#L520-L535)）:

```go
type ExecState struct {
	State
	exec *ExecOp
}
func (e ExecState) Root() State { return e.State }  // 実行後 rootfs を State として取り出す
func (e ExecState) AddMount(target string, source State, opt ...MountOption) State {
	return source.WithOutput(e.exec.AddMount(target, source.Output(), opt...))
}
```

プロジェクトのコードと直結する:

```go
// internal/llbgen/llbgen.go（コマンドのチェーン）
run := st.Run(llb.Shlex(cmd), ...)                  // ExecState を得る
run.AddMount(workdir, srcState, llb.Readonly)       // source を入力に追加 → DAG の辺
run.AddMount(cachePath, llb.Scratch(), ...)         // キャッシュマウント
run.AddMount("/.btr/deps/"+dep, ds, llb.Readonly)   // 依存 → ordering edge
st = run.Root()                                      // 次コマンドの土台にする（チェーン）
```

- `run.Root()` で「このコマンド実行後の rootfs」を `State` として取り出し、次の `Run` の土台にする
  → **cmds が順番に積み上がる**。
- `AddMount` の `source State` は、その Output が Exec の **Inputs** になる → **依存の辺**ができる
  （だから依存タスクが先に解決される）。

---

## 5. `Marshal`：`State` → `Definition`

最後に `State.Marshal` がグラフを protobuf に平坦化する（[state.go#L140](https://github.com/moby/buildkit/blob/v0.30.0/client/llb/state.go#L140)）:

```go
def, err := marshal(ctx, s.Output().Vertex(ctx, c), def, ...)  // 出力頂点から再帰的に辿る
...
def.Def = append(def.Def, dt)   // 各頂点を pb.Op にして [][]byte に
```

- `s.Output().Vertex()` を起点に **DAG を再帰的に辿り**、各頂点を `pb.Op` にして `Definition.Def [][]byte` に詰める。
- 各頂点は **digest（内容ハッシュ）**で識別され、同内容の頂点は1つに重複排除される。
- この digest が **コンテンツアドレッサブルキャッシュ**の鍵。入力が同じなら同じ digest → buildkitd 側でキャッシュヒット。

本プロジェクトでは `runner.Run` 内の `st.Marshal(ctx, platform)` がこれを呼び、得た `*llb.Definition` を
`client.Solve` に渡す。`--debug` で出る `marshaled LLB definition (N operations in the graph)` の N は `len(def.Def)`（頂点数）。

---

## まとめ（全体像）

```
llb.Image(...)            → out=SourceVertex の State
   .Dir("/src")           → メタチェーンに keyDir を積んだ新 State（頂点は増えない）
   .AddEnv(...)           → メタチェーンに keyEnv を積んだ新 State
   .Run(cmd)              → ExecState（out=ExecOp の出力）
       .AddMount(...)     → Exec の Inputs に source を追加 = DAG の辺
       .Root()            → 実行後 rootfs の State（次へチェーン）
   ...
   .Marshal(ctx, plat)    → DAG を辿って pb.Op[] に平坦化 = *Definition（digest で重複排除）
                          → client.Solve に渡して初めて実行
```

`State` を一言で再定義すると、**「①出力を生む頂点への参照（操作 DAG）と、②環境メタデータの連結リストを、
両方まとめて運ぶイミュータブルなビルダー値」**。これを `Run`/`AddMount` で繋いでグラフを作り、
`Marshal` で確定し、`Solve` で初めて走らせる——というのが LLB の流れ。

## さらに掘るなら

- `exec.go` — `ExecOp` の Marshal とマウントの pb 表現
- `source.go` — `Image` / `Local` の Vertex 化
- `definition.go` — `Definition` の構造
