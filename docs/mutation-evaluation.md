# Avaliação do Gremlins no pacote `./consensus`

Relatório de avaliação da ferramenta de teste de mutação `gremlins` (`v0.6.0`) executada no pacote `./consensus` (commit `3502d54` da ENG-12).

## Configuração da Execução

- **Comando:** `~/go/bin/gremlins unleash --timeout-coefficient 20 ./consensus`
- **Ambiente/Commit de Validação:** `3502d54` (validação da interface de consenso)
- **Log de Execução:** `/tmp/ENG-52-gremlins-consensus.log`
- **Tempo de Cobertura:** 56.689ms
- **Tempo de Parede Total:** 1 segundo 199 milissegundos

## Métricas e Contagens

| Métrica | Contagem | Percentual |
| :--- | :--- | :--- |
| **Mutantes Gerados (Total)** | 13 | 100.00% |
| **Mortos (Killed)** | 8 | 61.54% |
| **Sobreviventes (Lived)** | 3 | 23.08% |
| **Timed Out** | 2 | 15.38% |
| **Not Covered** | 0 | 0.00% |
| **Mutator Coverage** | - | 100.00% |
| **Test Efficacy** | - | 72.73% |

## Lista de Sobreviventes (Lived)

1. **`pow.go:43:30`** — `CONDITIONALS_BOUNDARY`
   - Trecho: `if err != nil || difficulty < 0`
   - Mutação: `difficulty < 0` alterado para `difficulty <= 0`.
2. **`pow.go:84:16`** — `CONDITIONALS_BOUNDARY`
   - Trecho: `if difficulty < 0`
   - Mutação: `difficulty < 0` alterado para `difficulty <= 0`.
3. **`pow.go:90:28`** — `INCREMENT_DECREMENT`
   - Trecho: `for b.Nonce = 0; ; b.Nonce++`
   - Mutação: `b.Nonce++` alterado para `b.Nonce--`.

## Mutações com Timeout (Timed Out)

1. **`pow.go:80:9`** — `CONDITIONALS_NEGATION`
   - Trecho: `if ctx == nil`
   - Efeito: Quando negado (`ctx != nil`), substitui `ctx` por `context.Background()`, ignorando timeouts/cancelamentos e causando loop infinito no teste de cancelamento.
2. **`pow.go:91:28`** — `CONDITIONALS_NEGATION`
   - Trecho: `if err := ctx.Err(); err != nil`
   - Efeito: Quando negado (`err == nil`), impede o retorno do erro de cancelamento do contexto durante o laço de mineração, provocando loop infinito até o timeout da execução.
