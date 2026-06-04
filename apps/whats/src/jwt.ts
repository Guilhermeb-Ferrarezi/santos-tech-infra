import jwt from "jsonwebtoken"
import { config } from "./config"

// Token de serviço HS256 (mesmo JWT_SECRET do ecossistema). Por padrão assume a
// conta admin do dono (agent-go); a escalação usa a conta do Claude (sub próprio).
// As claims são as mesmas do auth central: {sub, iat, exp}.
export function serviceToken(sub: number = config.ownerUserID): string {
  const now = Math.floor(Date.now() / 1000)
  return jwt.sign({ sub: String(sub), iat: now, exp: now + 600 }, config.jwtSecret, { algorithm: "HS256" })
}
