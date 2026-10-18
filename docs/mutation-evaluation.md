# Avaliação do Gremlins no pacote `./consensus`

Relatório de avaliação da ferramenta de teste de mutação `gremlins` (`v0.6.0`).

## Contexto e Ambiente de Validação

- **Commit de Validação:** A validação foi executada no commit `3502d54` (da branch `refactor/ENG-12-consensus-interface`), pois a presente branch (`chore/ENG-51-mutation-testing`, baseada em `origin/master` `9e34565`) ainda não contém o pacote `./consensus`.
- **Comando Fixado (Execução por Pacote):**
  `go run github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0 unleash --timeout-coefficient 20 ./consensus`
- **Tempo de Coleta de Cobertura:** 56.689ms
- **Tempo de Parede Total:** 1 segundo 199 milissegundos

## Operadores Ativos (Gremlins v0.6.0)

Operadores padrão habilitados durante a execução:
- `ARITHMETIC_BASE` (+, -, *, /, %)
- `CONDITIONALS_BOUNDARY` (< para <=, > para >=, etc.)
- `CONDITIONALS_NEGATION` (== para !=, != para ==, etc.)
- `INCREMENT_DECREMENT` (++ para --, -- para ++)
- `INVERT_NEGATIVES` (-x para x)

*(Operadores opcionais da ferramenta não habilitados por padrão: `INVERT_ASSIGNMENTS`, `INVERT_BITWISE`, `INVERT_BWASSIGN`, `INVERT_LOGICAL`, `INVERT_LOOPCTRL`, `REMOVE_SELF_ASSIGNMENTS`).*

## Métricas e Contagens (`./consensus`)

| Métrica | Contagem | Percentual |
| :--- | :--- | :--- |
| **Mutantes Gerados (Total)** | 13 | 100.00% |
| **Mortos (Killed)** | 8 | 61.54% |
| **Sobreviventes (Lived)** | 3 | 23.08% |
| **Timed Out** | 2 | 15.38% |
| **Not Covered** | 0 | 0.00% |
| **Mutator Coverage** | - | 100.00% |
| **Test Efficacy** | - | 72.73% |

### Detalhamento dos Sobreviventes (Lived)
Os 3 mutantes sobreviventes são **reais e não equivalentes**, e o seu tratamento e eliminação ficam atribuídos à subtarefa **ENG-61** (`./consensus`):
1. **`pow.go:43:30`** — `CONDITIONALS_BOUNDARY`: `if err != nil || difficulty < 0` mutado para `difficulty <= 0`.
2. **`pow.go:84:16`** — `CONDITIONALS_BOUNDARY`: `if difficulty < 0` mutado para `difficulty <= 0`.
3. **`pow.go:90:28`** — `INCREMENT_DECREMENT`: `for b.Nonce = 0; ; b.Nonce++` mutado para `b.Nonce--`.

### Detalhamento dos Mutantes com Timeout (Timed Out)
Mutantes que induzem loops infinitos em testes de cancelamento/timeout:
1. **`pow.go:80:9`** — `CONDITIONALS_NEGATION`: `if ctx == nil` negado substitui `ctx` por `context.Background()`, ignorando cancelamento e provocando loop infinito no teste de cancelamento.
2. **`pow.go:91:28`** — `CONDITIONALS_NEGATION`: `if err := ctx.Err(); err != nil` negado impede o encerramento do laço de mineração no cancelamento do contexto.

*(Conforme regra do AGENTS.md, `TIMED OUT` conta como morto/Killed).*

## Análise dos Achados da Revisão (QA-Sonnet)

- **Achado 1 (`server/server.go:52-54`):** Troca de `NewProofOfWorkFromEnv()` por `NewProofOfWork(DefaultDifficulty)`. Não avaliado nesta rodada por escopo reduzido; fica a cargo da subtarefa **ENG-59** (`./server`).
- **Achado 2 (`consensus/pow.go:57-63`):** Inversão da ordem das checagens `Index` e `PrevHash` no `Verify`. O `gremlins` não possui operador que permute ou reordene statements adjacentes, de modo que **não gera essa mutação**. Por essa razão, a ferramenta funciona como um **gate parcial**, sendo os mutantes manuais preservados e necessários para cobrir essa classe de regras estruturais.

## Avaliação do Modo `--diff` e Risco de Falso Verde

### 1. Falso Verde ao Combinar `--diff` com Pacote
Quando a flag `--diff` é combinada com um caminho de pacote, o `gremlins` não correlaciona os caminhos do diff git com o pacote filtrado, resultando em 100% de mutantes ignorados (`Skipped`) e relatório de falso verde ("0 lived"):
```
$ go run github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0 unleash --timeout-coefficient 20 --diff 9e34565 ./consensus
Starting...
Gathering files diff...
Gathering coverage... done in 57.489817ms
     SKIPPED CONDITIONALS_NEGATION at pow.go:43:9
     SKIPPED CONDITIONALS_NEGATION at pow.go:38:9
     SKIPPED CONDITIONALS_NEGATION at pow.go:43:30
     SKIPPED CONDITIONALS_BOUNDARY at pow.go:43:30
     SKIPPED CONDITIONALS_NEGATION at pow.go:84:16
     SKIPPED ARITHMETIC_BASE at pow.go:57:15
     SKIPPED CONDITIONALS_NEGATION at pow.go:65:23
     SKIPPED CONDITIONALS_NEGATION at pow.go:61:15
     SKIPPED INCREMENT_DECREMENT at pow.go:90:28
     SKIPPED CONDITIONALS_NEGATION at pow.go:57:18
     SKIPPED CONDITIONALS_BOUNDARY at pow.go:84:16
     SKIPPED CONDITIONALS_NEGATION at pow.go:91:28
     SKIPPED CONDITIONALS_NEGATION at pow.go:80:9

Mutation testing completed in 52 milliseconds 479 microseconds
Killed: 0, Lived: 0, Not covered: 0
Timed out: 0, Not viable: 0, Skipped: 13
Test efficacy: 0.00%
Mutator coverage: 0.00%
```

### 2. Execução Correta do Modo `--diff` (na Raiz do Repositório, sem Pacote)
Executado na raiz do repositório comparando `3502d54` contra a base `9e34565`:
```
$ go run github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0 unleash --timeout-coefficient 20 --diff 9e34565
Starting...
Gathering files diff...
Gathering coverage... done in 313.378592ms
...
      KILLED ARITHMETIC_BASE at blockchain/blockchain.go:29:34
      KILLED CONDITIONALS_NEGATION at consensus/pow.go:84:16
       LIVED CONDITIONALS_BOUNDARY at consensus/pow.go:43:30
      KILLED CONDITIONALS_NEGATION at consensus/pow.go:43:9
      KILLED CONDITIONALS_NEGATION at consensus/pow.go:61:15
      KILLED CONDITIONALS_NEGATION at consensus/pow.go:43:30
      KILLED CONDITIONALS_NEGATION at server/server.go:125:14
       LIVED CONDITIONALS_BOUNDARY at consensus/pow.go:84:16
      KILLED CONDITIONALS_NEGATION at server/server.go:125:89
      KILLED ARITHMETIC_BASE at consensus/pow.go:57:15
      KILLED CONDITIONALS_NEGATION at consensus/pow.go:38:9
      KILLED CONDITIONALS_NEGATION at consensus/pow.go:57:18
      KILLED CONDITIONALS_NEGATION at consensus/pow.go:65:23
       LIVED INCREMENT_DECREMENT at consensus/pow.go:90:28
   TIMED OUT CONDITIONALS_NEGATION at consensus/pow.go:80:9
   TIMED OUT CONDITIONALS_NEGATION at consensus/pow.go:91:28

Mutation testing completed in 6 seconds 336 milliseconds
Killed: 11, Lived: 3, Not covered: 0
Timed out: 2, Not viable: 0, Skipped: 37
Test efficacy: 78.57%
Mutator coverage: 100.00%
```
*(37 mutantes fora das linhas alteradas pelo diff foram marcados como `Skipped`, enquanto todos os mutantes nas linhas adicionadas/modificadas foram avaliados).*

## ENG-61 — resultado após os testes

Comando (pacote `./consensus`, sem `--diff`):

```
$ go run github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0 unleash --timeout-coefficient 20 ./consensus
Starting...
Gathering coverage... done in 386.942454ms
      KILLED CONDITIONALS_NEGATION at pow.go:84:16
      KILLED CONDITIONALS_NEGATION at pow.go:65:23
       LIVED CONDITIONALS_BOUNDARY at pow.go:84:16
      KILLED CONDITIONALS_NEGATION at pow.go:38:9
      KILLED CONDITIONALS_NEGATION at pow.go:61:15
      KILLED CONDITIONALS_NEGATION at pow.go:57:18
      KILLED CONDITIONALS_NEGATION at pow.go:43:30
      KILLED INCREMENT_DECREMENT at pow.go:90:28
      KILLED CONDITIONALS_NEGATION at pow.go:43:9
      KILLED CONDITIONALS_BOUNDARY at pow.go:43:30
      KILLED CONDITIONALS_NEGATION at pow.go:91:28
      KILLED ARITHMETIC_BASE at pow.go:57:15
      KILLED CONDITIONALS_NEGATION at pow.go:80:9

Mutation testing completed in 4 seconds 21 milliseconds
Killed: 12, Lived: 1, Not covered: 0
Timed out: 0, Not viable: 0, Skipped: 0
Test efficacy: 92.31%
Mutator coverage: 100.00%
```

- `pow.go:43:30`, `pow.go:90:28`: mortos por testes novos (`POW_DIFFICULTY=0`; menor nonce não-negativo).
- `pow.go:80:9`, `pow.go:91:28`: antes `TIMED OUT`; agora `KILLED` (contexto já cancelado com dificuldade 0; os testes de cancelamento em andamento/deadline usam `mineAsync`, que falha em 1s em vez de travar).
- `pow.go:84:16` (`difficulty < 0` → `<= 0`): **equivalente** — com `difficulty == 0` o ramo atribui `0`, sem efeito observável. Aguarda concordância do revisor.

## ENG-59 — `./server` após os testes

Comando (sobre `test/ENG-59-mutation-server`, base `7c7a940`):

```
$ go run github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0 unleash --timeout-coefficient 20 ./server
go: downloading github.com/go-gremlins/gremlins v0.6.0
go: downloading github.com/spf13/cobra v1.10.2
go: downloading github.com/bluekeyes/go-gitdiff v0.8.1
go: downloading github.com/mitchellh/go-homedir v1.1.0
go: downloading golang.org/x/tools v0.39.0
go: downloading github.com/spf13/viper v1.21.0
go: downloading github.com/fatih/color v1.18.0
go: downloading github.com/spf13/pflag v1.0.10
go: downloading github.com/MakeNowJust/heredoc v1.0.0
go: downloading github.com/hako/durafmt v0.0.0-20210608085754-5c1018a4e16b
go: downloading github.com/mattn/go-colorable v0.1.14
go: downloading github.com/mattn/go-isatty v0.0.20
go: downloading github.com/fsnotify/fsnotify v1.9.0
go: downloading github.com/spf13/cast v1.10.0
go: downloading go.yaml.in/yaml/v3 v3.0.4
go: downloading github.com/sagikazarmark/locafero v0.12.0
go: downloading github.com/subosito/gotenv v1.6.0
go: downloading github.com/pelletier/go-toml/v2 v2.2.4
go: downloading github.com/spf13/afero v1.15.0
go: downloading github.com/go-viper/mapstructure/v2 v2.4.0
go: downloading golang.org/x/sys v0.38.0
go: downloading golang.org/x/text v0.31.0
Starting...
Gathering coverage... done in 11.578454491s
 NOT COVERED ARITHMETIC_BASE at server.go:22:33
      KILLED ARITHMETIC_BASE at server.go:83:23
      KILLED ARITHMETIC_BASE at server.go:42:11
      KILLED CONDITIONALS_NEGATION at server.go:131:9
      KILLED ARITHMETIC_BASE at server.go:86:22
      KILLED CONDITIONALS_NEGATION at server.go:107:9
      KILLED CONDITIONALS_NEGATION at server.go:59:41
      KILLED ARITHMETIC_BASE at server.go:60:30
      KILLED CONDITIONALS_BOUNDARY at server.go:59:56
      KILLED CONDITIONALS_NEGATION at server.go:119:9
      KILLED CONDITIONALS_BOUNDARY at server.go:55:56
      KILLED CONDITIONALS_NEGATION at server.go:90:36
      KILLED CONDITIONALS_NEGATION at server.go:59:56
      KILLED CONDITIONALS_NEGATION at server.go:149:14
      KILLED CONDITIONALS_NEGATION at server.go:179:36
      KILLED CONDITIONALS_NEGATION at server.go:150:98
      KILLED CONDITIONALS_NEGATION at server.go:193:25
      KILLED ARITHMETIC_BASE at server.go:85:22
      KILLED CONDITIONALS_NEGATION at server.go:55:56
      KILLED CONDITIONALS_NEGATION at server.go:51:9
      KILLED CONDITIONALS_NEGATION at server.go:55:44
      KILLED CONDITIONALS_NEGATION at server.go:205:47
      KILLED CONDITIONALS_NEGATION at server.go:161:14
      KILLED CONDITIONALS_NEGATION at server.go:161:89
      KILLED CONDITIONALS_NEGATION at server.go:202:27
      KILLED CONDITIONALS_NEGATION at server.go:222:36
      KILLED CONDITIONALS_NEGATION at server.go:227:11
      KILLED CONDITIONALS_BOUNDARY at server.go:227:11

Mutation testing completed in 24 seconds 665 milliseconds
Killed: 27, Lived: 0, Not covered: 1
Timed out: 0, Not viable: 0, Skipped: 0
Test efficacy: 100.00%
Mutator coverage: 96.43%
```

Resultado: `Lived: 0`, `Timed out: 0`, `Skipped: 0`. Único `NOT COVERED`: `server.go:22:33` (`const DefaultMiningTimeout = 10 * time.Second`). Justificativa: uma linha de `const` não tem bloco de cobertura, então o gremlins não consegue instrumentá-la; o valor é fixado por asserção (`DefaultMiningTimeout == 10*time.Second`) em `server_test.go`. Limitação da ferramenta, sujeita à concordância do revisor.
