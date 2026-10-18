# AGENTS.md — blockchain-with-go

Diretrizes para agentes de IA atuando no repositório `blockchain-with-go`.

## Contexto do Projeto

Implementação didática de blockchain em Go com validação de blocos, hashing SHA-256 e servidor HTTP simples (`gorilla/mux`).

- Para arquitetura completa, conceitos de consenso e trade-offs, consulte o [README.md](README.md).

## Estrutura do Repositório

```
blockchain-with-go/
├── cmd/
│   └── node/          # Ponto de entrada da aplicação (main.go)
├── internal/
│   ├── chain/         # chain.Service (Blocks/Mine), geração de blocos, ErrInvalidBlock
│   ├── consensus/     # Porta Consensus e erros
│   │   └── pow/       # Adaptador Proof of Work
│   ├── domain/        # Modelos de domínio (Block, Transaction, regras) — sem dependências internas
│   ├── server/        # Adaptador HTTP: server.Server (DI), handlers, DTOs e mapeamento de erros
│   └── storage/       # Porta BlockRepository
│       └── memory/    # Adaptador em memória (sync.RWMutex)
├── pkg/
│   └── wallet/        # Chaves Ed25519, endereçamento e keystore
├── go.mod             # Módulo Go e dependências
└── README.md          # Documentação do projeto e instruções de execução
```

## Arquitetura (Ports & Adapters)

- Dependências apontam para dentro: `cmd/node` → `server`/`chain`/adaptadores → `domain`. `internal/domain` nunca importa `storage`, `chain`, `server`, `net/http` nem `os/signal`; `internal/chain` não importa `server` nem `net/http`.
- Sem estado global: `internal/server` e `internal/chain` só têm `const` e sentinelas `errors.New` no nível de pacote. Dependências (repositório, consenso, gerador de blocos) entram por construtor (`chain.NewService(repo, consensus, opts...)`, `server.NewServer(svc, cfg)`).
- Composition root em `cmd/node/main.go`; é o único lugar que dispara a leitura de configuração (`PORT` e `MINING_TIMEOUT` diretamente; `POW_DIFFICULTY` via `pow.NewProofOfWorkFromEnv`).

## Regras e Comandos

1. **Validação e Testes:**
   - Executar testes e checagem de tipos: `go test ./...`
   - Formatação padrão de Go: `gofmt -s -w .` ou `goimports`
   - Verificação de compilação: `go build -o /dev/null ./cmd/node`
   - Execução local do nó: `go run ./cmd/node`
   - Teste de mutação (gremlins v0.6.0) — duas formas suportadas:
     - Por pacote: `go run github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0 unleash --timeout-coefficient 20 ./internal/<pacote>` (ou `./pkg/wallet`)
     - Por diff: `go run github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0 unleash --timeout-coefficient 20 --diff <base>` (executado na raiz e **sem** pacote)
     - **Regra de execução:** nunca combinar `--diff` com um caminho de pacote (gera falso verde com todos os mutantes marcados como `Skipped`). Se `Skipped` for igual ao total, o resultado é inválido. Mutações com status `TIMED OUT` **não** contam como mortas: o gremlins não as classificou. Rode `go clean -testcache` e repita antes de aceitar o resultado. Com o cache de testes ativo, a rodada de cobertura leva milissegundos, o limite de tempo fica minúsculo e quase todo mutante vira `TIMED OUT` (falso verde).
     - **Resultados não são gravados no repositório:** cole a saída do gremlins no comentário da issue e na descrição do PR. Não crie nem atualize arquivos em `docs/` para registrá-la.
   - Limiar de mutação: zero mutantes `LIVED` ou `NOT COVERED` nas linhas alteradas pelo diff. Um mutante equivalente só é aceito se constar no relatório do QA com justificativa de uma linha e o revisor concordar. Mutante sobrevivente continua bloqueante.
2. **Boas Práticas:**
   - Manter código idiomático em Go (tratamento explícito de erros, nomenclatura concisa).
   - Não adicionar dependências pesadas sem necessidade justificável.
   - Mensagens de commit seguindo Conventional Commits (`feat:`, `fix:`, `docs:`, `refactor:`, `test:`).
   - Respostas ao usuário em Português do Brasil; código e commits em Inglês.
