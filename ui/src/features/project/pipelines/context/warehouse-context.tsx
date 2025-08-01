import { createContext, useContext } from 'react';
import { Warehouse } from '@ui/gen/api/v1alpha1/generated_pb';

export const WarehouseContext = createContext<Warehouse[]>([]);

export const useWarehouses = () => {
  const warehouses = useContext(WarehouseContext);
  return warehouses;
};
