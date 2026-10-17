# AGENTS.md — blockchain-with-go

Diretrizes para agentes de IA atuando no repositório `blockchain-with-go`.

## Contexto do Projeto

Implementação didática de blockchain em Go com validação de blocos, hashing SHA-256 e servidor HTTP simples (`gorilla/mux`).

- Para arquitetura completa, conceitos de consenso e trade-offs, consulte o [README.md](README.md).

## Estrutura do Repositório

```
blockchain-with-go/
├── main.go         # Inicialização do servidor e porta HTTP
├── blockchain/     # Regras de cadeia, blocos e validação
├── models/         # Estruturas de dados (Block, Message, etc.)
├── server/         # Handlers e rotas HTTP (gorilla/mux)
├── go.mod          # Módulo Go e dependências
└── README.md       # Documentação do projeto e instruções de execução
```

## Regras e Comandos

1. **Validação e Testes:**
   - Executar testes e checagem de tipos: `go test ./...`
   - Formatação padrão de Go: `gofmt -s -w .` ou `goimports`
   - Verificação de compilação: `go build -o /dev/null .`
   - Teste de mutação (gremlins v0.6.0) — duas formas suportadas:
     - Por pacote: `go run github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0 unleash --timeout-coefficient 20 ./<pacote>`
     - Por diff: `go run github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0 unleash --timeout-coefficient 20 --diff <base>` (executado na raiz e **sem** pacote)
     - **Regra de execução:** nunca combinar `--diff` com um caminho de pacote (gera falso verde com todos os mutantes marcados como `Skipped`). Se `Skipped` for igual ao total, o resultado é inválido. Mutações com status `TIMED OUT` contam como mortas (Killed).
   - Limiar de mutação: zero mutantes `LIVED` ou `NOT COVERED` nas linhas alteradas pelo diff. Um mutante equivalente só é aceito se constar no relatório do QA com justificativa de uma linha e o revisor concordar. Mutante sobrevivente continua bloqueante.
2. **Boas Práticas:**
   - Manter código idiomático em Go (tratamento explícito de erros, nomenclatura concisa).
   - Não adicionar dependências pesadas sem necessidade justificável.
   - Mensagens de commit seguindo Conventional Commits (`feat:`, `fix:`, `docs:`, `refactor:`, `test:`).
   - Respostas ao usuário em Português do Brasil; código e commits em Inglês.
