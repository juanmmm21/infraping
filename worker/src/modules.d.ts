// wrangler empaqueta los .yaml como texto (regla "Text" en wrangler.jsonc).
declare module "*.yaml" {
  const text: string;
  export default text;
}

// Los tests leen los ejemplos con el sufijo ?raw de Vite, sin depender de los tipos de Node.
declare module "*?raw" {
  const text: string;
  export default text;
}
