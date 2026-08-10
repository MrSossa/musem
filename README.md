# musem

## Flujo de trabajo: GitHub flow

`main` está protegida: **no se puede hacer push directo ni merge sin PR**.

1. Crea una rama desde `main`:

   ```bash
   git switch main && git pull
   git switch -c feat/mi-cambio
   ```

2. Commitea y sube la rama:

   ```bash
   git push -u origin feat/mi-cambio
   ```

3. Abre el PR:

   ```bash
   gh pr create --fill
   ```

4. Antes de mergear:
   - la rama debe estar al día con `main` (`gh pr update-branch` o `git merge main`),
   - todas las conversaciones del PR deben estar resueltas.

5. Mergea y limpia:

   ```bash
   gh pr merge --squash --delete-branch
   ```

### Reglas activas sobre `main`

- Requiere pull request para cualquier cambio.
- Requiere que los hilos de conversación estén resueltos.
- Requiere la rama actualizada respecto a `main`.
- Prohibido el force-push y borrar `main`.
